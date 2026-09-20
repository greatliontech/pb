// Package rules parses and validates rule files — the `*.rules.yaml`
// files of a ruleset (check-rules.md REQ-rules-file-schema,
// REQ-rules-file-discovery) — and refuses any whose CEL environment
// version the engine does not provide (REQ-rules-env-versioned), so no
// rule is ever evaluated against an environment it did not target.
// Parsing walks the YAML AST so the accepted surface is the schema's,
// exactly as the other contract files do; a rule's CEL is kept as
// written and compiled by the environment, not here.
package rules

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/contractfile"
)

// Suffix names a rule file: every file so named under a ruleset's
// module root is one (REQ-rules-file-discovery).
const Suffix = ".rules.yaml"

// ErrInvalid is wrapped by every schema rejection.
var ErrInvalid = errors.New("invalid rule file")

// ErrEnvironment is wrapped when a rule file targets an environment
// version the engine does not provide: a file that is valid and yet
// unusable, so it does not wrap ErrInvalid.
var ErrEnvironment = errors.New("unprovided CEL environment")

// decimal is the one spelling of an environment version.
var decimal = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// keys are a rule entry's keys.
var keys = map[string]bool{"id": true, "kind": true, "target": true, "severity": true, "tags": true, "cel": true, "message": true}

// File is one parsed rule file.
type File struct {
	// CELEnv is the environment version every rule of the file
	// targets, one the engine provides.
	CELEnv int
	// Rules are the file's rules in declaration order.
	Rules []Rule
}

// Rule is one declared check (check-rules.md, the rule term).
type Rule struct {
	ID       string
	Kind     check.Kind
	Target   check.Target
	Severity check.Severity
	Tags     []string
	CEL      string // the expression as written
	Message  string
}

// Parse parses and validates one rule file's bytes.
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing celEnv and rules keys", ErrInvalid)
	}
	f := &File{}
	var haveEnv, haveRules bool
	for _, kv := range mapping.Values {
		key := contractfile.Key(kv.Key)
		switch key {
		case "celEnv":
			// Unquoted decimal digits, no sign, no leading zero: the
			// version is a number, and one number has one spelling here.
			// The parser types a plain scalar of many digits as a string,
			// so the spelling is judged on the token, not the node; a
			// value no int holds is a version the engine does not provide.
			tok := kv.Value.GetToken()
			if tok.Type == token.SingleQuoteType || tok.Type == token.DoubleQuoteType || !decimal.MatchString(tok.Value) {
				return nil, fmt.Errorf("%w: celEnv must be an unquoted decimal integer", ErrInvalid)
			}
			v, err := strconv.Atoi(tok.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: celEnv %s (provided: %v)", ErrEnvironment, tok.Value, check.Environments())
			}
			f.CELEnv, haveEnv = v, true
		case "rules":
			rs, err := parseRules(kv.Value)
			if err != nil {
				return nil, err
			}
			f.Rules, haveRules = rs, true
		default:
			return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, key)
		}
	}
	if !haveEnv {
		return nil, fmt.Errorf("%w: missing celEnv key", ErrInvalid)
	}
	if !haveRules {
		return nil, fmt.Errorf("%w: missing rules key", ErrInvalid)
	}
	if !check.ProvidesEnvironment(f.CELEnv) {
		return nil, fmt.Errorf("%w: celEnv %d (provided: %v)", ErrEnvironment, f.CELEnv, check.Environments())
	}
	return f, nil
}

func parseRules(n ast.Node) ([]Rule, error) {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return nil, fmt.Errorf("%w: rules must be a list", ErrInvalid)
	}
	rules := make([]Rule, 0, len(seq.Values))
	seen := map[string]bool{}
	for i, en := range seq.Values {
		em, ok := en.(*ast.MappingNode)
		if !ok {
			return nil, fmt.Errorf("%w: rules[%d] must be a mapping", ErrInvalid, i)
		}
		var r Rule
		have := map[string]bool{}
		for _, kv := range em.Values {
			key := contractfile.Key(kv.Key)
			if !keys[key] {
				return nil, fmt.Errorf("%w: rules[%d]: unknown key %q", ErrInvalid, i, key)
			}
			have[key] = true // a duplicate key never reaches here: the parser refuses it
			switch key {
			case "tags":
				tags, err := parseTags(kv.Value)
				if err != nil {
					return nil, fmt.Errorf("%w: rules[%d]: %v", ErrInvalid, i, err)
				}
				r.Tags = tags
				continue
			}
			// The expression is text, a block scalar its readable
			// spelling; every other field is one line — an id is written
			// in a suppression comment, a message on a finding line.
			text, ok := contractfile.Line(kv.Value)
			if key == "cel" {
				text, ok = contractfile.Scalar(kv.Value)
			}
			if !ok || text == "" {
				if key == "cel" {
					return nil, fmt.Errorf("%w: rules[%d]: cel must be a non-empty scalar", ErrInvalid, i)
				}
				return nil, fmt.Errorf("%w: rules[%d]: %s must be one non-empty line of text", ErrInvalid, i, key)
			}
			switch key {
			case "id":
				r.ID = text
			case "kind":
				k, ok := check.ParseKind(text)
				if !ok {
					return nil, fmt.Errorf("%w: rules[%d]: kind %q is not %s or %s", ErrInvalid, i, text, check.KindLint, check.KindBreaking)
				}
				r.Kind = k
			case "target":
				tg, ok := check.ParseTarget(text)
				if !ok {
					return nil, fmt.Errorf("%w: rules[%d]: target %q is not one of %v", ErrInvalid, i, text, check.Targets())
				}
				r.Target = tg
			case "severity":
				sv, ok := check.ParseSeverity(text)
				if !ok {
					return nil, fmt.Errorf("%w: rules[%d]: severity %q is not %s or %s", ErrInvalid, i, text, check.SeverityError, check.SeverityWarning)
				}
				r.Severity = sv
			case "cel":
				r.CEL = text
			case "message":
				r.Message = text
			default:
				return nil, fmt.Errorf("%w: rules[%d]: unknown key %q", ErrInvalid, i, key)
			}
		}
		for _, k := range []string{"id", "kind", "target", "severity", "cel", "message"} {
			if !have[k] {
				return nil, fmt.Errorf("%w: rules[%d]: missing %s key", ErrInvalid, i, k)
			}
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("%w: rules[%d]: id %q declared twice", ErrInvalid, i, r.ID)
		}
		seen[r.ID] = true
		rules = append(rules, r)
	}
	return rules, nil
}

func parseTags(n ast.Node) ([]string, error) {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return nil, errors.New("tags must be a list")
	}
	tags := make([]string, 0, len(seq.Values))
	for _, v := range seq.Values {
		text, ok := contractfile.Line(v)
		if !ok || text == "" {
			return nil, errors.New("tags must be non-empty lines of text")
		}
		tags = append(tags, text)
	}
	return tags, nil
}

// Located is a rule file with its path within the ruleset.
type Located struct {
	Path string
	File *File
}

// Discover reads every rule file under root in fsys — every file named
// with Suffix at any depth, in path order, the byte order of the full
// paths — and parses each; a file that fails to parse fails the
// discovery naming it (REQ-rules-file-discovery). A ruleset with none
// yields no files.
func Discover(fsys fs.FS, root string) ([]Located, error) {
	var paths []string
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, Suffix) {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The walk orders per directory; the contract orders by path.
	sort.Strings(paths)
	out := make([]Located, 0, len(paths))
	for _, p := range paths {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		f, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, Located{Path: p, File: f})
	}
	return out, nil
}
