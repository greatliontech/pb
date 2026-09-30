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
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/contractfile"
)

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

// File is one parsed rule file (REQ-rules-file-schema).
type File struct {
	// CELEnv is the environment version every expression of the file
	// targets, one the engine provides.
	CELEnv int
	// Imports are the rulesets the file imports for their functions,
	// in declaration order (REQ-rules-imports).
	Imports []Import
	// Functions are the file's functions in declaration order
	// (REQ-rules-functions).
	Functions []Function
	// Rules are the file's rules in declaration order.
	Rules []Rule
	// Scope is what the file's expressions see: its functions and its
	// imports', shared by every rule of the file.
	Scope *Scope
}

// Rule is one declared check (check-rules.md, the rule term).
type Rule struct {
	// The id the rule file declares, and the alias the lint file
	// imports the declaring ruleset under, set when the rule is
	// imported; the two spell the rule's name.
	ID       string
	Ruleset  string
	Kind     check.Kind
	Target   check.Target
	Severity check.Severity
	Tags     []string
	CEL      string // the expression as written
	Message  string
	// Scope is the declaring file's: the functions the expression may
	// call (REQ-rules-functions).
	Scope *Scope
}

// Name is the rule's canonical name, `<module path>:<id>`, or the
// bare id for a rule no ruleset imported.
func (r Rule) Name() string {
	if r.Ruleset == "" {
		return r.ID
	}
	return r.Ruleset + ":" + r.ID
}

// Parse reads a rule file (REQ-rules-file-schema): the document a
// mapping of celEnv, rules and, optionally, imports and functions,
// every key known, every scalar the text written; the environment
// version refused unless provided (REQ-rules-env-versioned). The
// file's own scope is built and shared by its rules — the ruleset's,
// shared by its files, Discover's to build, its imports' lent
// functions the reader of the imports' to fill.
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing celEnv and rules", ErrInvalid)
	}
	f := &File{}
	err = contractfile.Mapping(mapping, "", ErrInvalid,
		contractfile.Field{Name: "celEnv", Required: true, Read: func(n ast.Node) error {
			// Unquoted decimal digits, no sign, no leading zero: the
			// version is a number, and one number has one spelling here.
			// The parser types a plain scalar of many digits as a string,
			// so the spelling is judged on the token, not the node; a
			// value no int holds is a version the engine does not provide.
			tok := n.GetToken()
			if tok.Type == token.SingleQuoteType || tok.Type == token.DoubleQuoteType || !decimal.MatchString(tok.Value) {
				return fmt.Errorf("%w: celEnv must be an unquoted decimal integer", ErrInvalid)
			}
			v, err := strconv.Atoi(tok.Value)
			if err != nil {
				return fmt.Errorf("%w: celEnv %s (provided: %v)", ErrEnvironment, tok.Value, check.Environments())
			}
			f.CELEnv = v
			return nil
		}},
		contractfile.Field{Name: "imports", Read: func(n ast.Node) error {
			imports, err := ParseImports(n, "imports", ErrInvalid)
			f.Imports = imports
			return err
		}},
		contractfile.Field{Name: "functions", Read: func(n ast.Node) error {
			fns, err := parseFunctions(n)
			f.Functions = fns
			return err
		}},
		contractfile.Field{Name: "rules", Required: true, Read: func(n ast.Node) error {
			rs, err := parseRules(n)
			f.Rules = rs
			return err
		}},
	)
	if err != nil {
		return nil, err
	}
	if !check.ProvidesEnvironment(f.CELEnv) {
		return nil, fmt.Errorf("%w: celEnv %d (provided: %v)", ErrEnvironment, f.CELEnv, check.Environments())
	}
	f.Scope = &Scope{Functions: f.Functions, Imports: f.Imports}
	for i := range f.Rules {
		f.Rules[i].Scope = f.Scope
	}
	return f, nil
}

// parseRules reads the rules list: each entry a mapping of the seven
// keys, six required, tags optional; the expression text in any
// scalar spelling, every other value one line of text; ids unique.
func parseRules(n ast.Node) ([]Rule, error) {
	rules := []Rule{}
	seen := map[string]bool{}
	err := contractfile.Sequence(n, "rules", ErrInvalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("rules[%d]", i)
		var r Rule
		line := func(name string, set func(string) error) contractfile.Field {
			return contractfile.Field{Name: name, Required: true, Read: func(n ast.Node) error {
				text, ok := contractfile.Line(n)
				if !ok || text == "" {
					return fmt.Errorf("%w: %s: %s must be one non-empty line of text", ErrInvalid, where, name)
				}
				return set(text)
			}}
		}
		err := contractfile.Mapping(en, where, ErrInvalid,
			line("id", func(s string) error {
				if strings.Contains(s, ":") {
					return fmt.Errorf("%w: %s: id %q holds a colon, the rule name's separator", ErrInvalid, where, s)
				}
				r.ID = s
				return nil
			}),
			line("kind", func(s string) error {
				k, ok := check.ParseKind(s)
				if !ok {
					return fmt.Errorf("%w: %s: kind %q is not %s or %s", ErrInvalid, where, s, check.KindLint, check.KindBreaking)
				}
				r.Kind = k
				return nil
			}),
			line("target", func(s string) error {
				tg, ok := check.ParseTarget(s)
				if !ok {
					return fmt.Errorf("%w: %s: target %q is not one of %v", ErrInvalid, where, s, check.Targets())
				}
				r.Target = tg
				return nil
			}),
			line("severity", func(s string) error {
				sv, ok := check.ParseSeverity(s)
				if !ok {
					return fmt.Errorf("%w: %s: severity %q is not %s or %s", ErrInvalid, where, s, check.SeverityError, check.SeverityWarning)
				}
				r.Severity = sv
				return nil
			}),
			contractfile.Field{Name: "tags", Read: func(n ast.Node) error {
				tags, err := contractfile.Strings(n, where+".tags", ErrInvalid)
				if err != nil {
					return err
				}
				for _, t := range tags {
					if strings.Contains(t, ":") {
						return fmt.Errorf("%w: %s: tag %q holds a colon, the rule name's separator", ErrInvalid, where, t)
					}
				}
				r.Tags = tags
				return nil
			}},
			contractfile.Field{Name: "cel", Required: true, Read: func(n ast.Node) error {
				// The expression is text, a block scalar its readable
				// spelling.
				text, ok := contractfile.Scalar(n)
				if !ok || text == "" {
					return fmt.Errorf("%w: %s: cel must be a non-empty scalar", ErrInvalid, where)
				}
				r.CEL = text
				return nil
			}},
			line("message", func(s string) error { r.Message = s; return nil }),
		)
		if err != nil {
			return err
		}
		if seen[r.ID] {
			return fmt.Errorf("%w: %s: id %q declared twice", ErrInvalid, where, r.ID)
		}
		seen[r.ID] = true
		rules = append(rules, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// Located is a rule file with its path within the ruleset.
type Located struct {
	Path string
	File *File
}

// Discover parses a ruleset's rule files — the module loader's map of
// every file named with module.RuleFileSuffix under the module root,
// at any depth, by module-relative path — in path order, the byte
// order of the paths; a file that fails to parse fails the discovery
// naming it (REQ-rules-file-discovery). A ruleset with none yields no
// files. The files share one scope, the ruleset's: its functions one
// namespace across its files, visible to every expression of the
// ruleset, and its imports one, an alias bound to one pair — a
// function name two files declare, or an alias two files bind to
// different pairs, fails the discovery naming both (REQ-rules-functions,
// REQ-rules-imports). The scope's Where is the ruleset's, left to the
// caller that knows it.
func Discover(files map[string][]byte) ([]Located, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]Located, 0, len(paths))
	scope := &Scope{}
	declaredIn := map[string]string{}
	aliasIn := map[string]Import{}
	for _, p := range paths {
		f, err := Parse(files[p])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for _, fn := range f.Functions {
			if prior, dup := declaredIn[fn.Name]; dup {
				return nil, fmt.Errorf("function %s declared by %s and %s", fn.Name, prior, p)
			}
			declaredIn[fn.Name] = p
			fn.File = p
			scope.Functions = append(scope.Functions, fn)
		}
		for _, imp := range f.Imports {
			if prior, bound := aliasIn[imp.Alias]; bound {
				if prior.Path != imp.Path || prior.Version != imp.Version {
					return nil, fmt.Errorf("alias %s bound to %s by %s and to %s by %s", imp.Alias, prior.pair(), prior.File, imp.pair(), p)
				}
				continue
			}
			imp.File = p
			aliasIn[imp.Alias] = imp
			scope.Imports = append(scope.Imports, imp)
		}
		f.Scope = scope
		for i := range f.Rules {
			f.Rules[i].Scope = scope
		}
		out = append(out, Located{Path: p, File: f})
	}
	return out, nil
}
