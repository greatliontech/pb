// Package trust parses and evaluates the trust policy file
// (pb.trust.yaml) — the resolution root's statement of which provenance
// evidence is required and which identities are accepted, for modules
// and plugin images alike (REQ-prov-trust-schema, REQ-prov-policy-eval).
// Parsing goes through the YAML AST so the accepted surface is the
// schema's, not the parser's, exactly as the other contract files do.
//
// Prefix matching is path-segment-aware: a rule prefix matches a
// subject that equals it or extends it at a "/" boundary — a plain
// string prefix would let "host/org" govern "host/organization", a
// scope the rule's author never granted.
package trust

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/greatliontech/pb/internal/yamlshape"
)

// FileName is the trust policy file, at the resolution root next to the
// module file.
const FileName = "pb.trust.yaml"

// Mode is a provenance requirement level.
type Mode string

const (
	AllowUnsigned     Mode = "allow-unsigned"
	RequireProvenance Mode = "require-provenance"
)

// IdentityRule is a rule's accepted identity: san is a glob pattern
// (* and ? wildcards) over certificate SANs, issuer an exact OIDC
// issuer string.
type IdentityRule struct {
	SAN    string
	Issuer string
}

// Rule is one trust policy rule. Prefix is matched segment-aware
// against module paths (modules list) or OCI references (plugins
// list); the empty prefix matches every subject. Require and Identity
// are each optional: an unset Require falls back to the policy default,
// and an unset Identity leaves identity acceptance to the
// origin-consistency default.
type Rule struct {
	Prefix   string
	Require  Mode          // "" = inherit the policy default
	Identity *IdentityRule // nil = origin-consistency default
}

// Policy is a parsed trust policy. The zero value is the empty policy:
// allow-unsigned for everything, no identity rules.
type Policy struct {
	Default Mode // "" = AllowUnsigned
	Modules []Rule
	Plugins []Rule
}

// ErrInvalid marks a trust policy violating the schema.
var ErrInvalid = errors.New("invalid trust policy")

// Parse decodes and validates a trust policy file
// (REQ-prov-trust-schema): top-level default/modules/plugins, each
// optional; rules of shape {prefix, require, identity: {san, issuer}};
// no unknown keys anywhere.
func Parse(data []byte) (*Policy, error) {
	astFile, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(astFile.Docs) != 1 {
		return nil, fmt.Errorf("%w: exactly one YAML document, got %d", ErrInvalid, len(astFile.Docs))
	}
	body := astFile.Docs[0].Body
	if body == nil {
		return &Policy{}, nil
	}
	if err := yamlshape.Check(body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	mapping, ok := body.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("%w: top level must be a mapping", ErrInvalid)
	}
	p := &Policy{}
	for _, kv := range mapping.Values {
		key := keyString(kv.Key)
		switch key {
		case "default":
			m, err := parseMode(kv.Value, "default")
			if err != nil {
				return nil, err
			}
			p.Default = m
		case "modules":
			rules, err := parseRules(kv.Value, "modules")
			if err != nil {
				return nil, err
			}
			p.Modules = rules
		case "plugins":
			rules, err := parseRules(kv.Value, "plugins")
			if err != nil {
				return nil, err
			}
			p.Plugins = rules
		default:
			return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, key)
		}
	}
	return p, nil
}

func keyString(n ast.Node) string {
	if s, ok := n.(*ast.StringNode); ok {
		return s.Value
	}
	return n.String()
}

func parseMode(n ast.Node, field string) (Mode, error) {
	s, ok := n.(*ast.StringNode)
	if !ok {
		return "", fmt.Errorf("%w: %s must be a string", ErrInvalid, field)
	}
	m := Mode(s.Value)
	if m != AllowUnsigned && m != RequireProvenance {
		return "", fmt.Errorf("%w: %s: unknown mode %q", ErrInvalid, field, s.Value)
	}
	return m, nil
}

func parseRules(n ast.Node, field string) ([]Rule, error) {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be a list", ErrInvalid, field)
	}
	seen := map[string]bool{}
	rules := make([]Rule, 0, len(seq.Values))
	for i, rn := range seq.Values {
		rm, ok := rn.(*ast.MappingNode)
		if !ok {
			return nil, fmt.Errorf("%w: %s[%d] must be a mapping", ErrInvalid, field, i)
		}
		var r Rule
		for _, kv := range rm.Values {
			key := keyString(kv.Key)
			switch key {
			case "prefix":
				s, ok := kv.Value.(*ast.StringNode)
				if !ok {
					return nil, fmt.Errorf("%w: %s[%d].prefix must be a string", ErrInvalid, field, i)
				}
				r.Prefix = s.Value
			case "require":
				m, err := parseMode(kv.Value, fmt.Sprintf("%s[%d].require", field, i))
				if err != nil {
					return nil, err
				}
				r.Require = m
			case "identity":
				id, err := parseIdentity(kv.Value, fmt.Sprintf("%s[%d].identity", field, i))
				if err != nil {
					return nil, err
				}
				r.Identity = id
			default:
				return nil, fmt.Errorf("%w: %s[%d]: unknown key %q", ErrInvalid, field, i, key)
			}
		}
		// A duplicate prefix would make "the longest matching rule"
		// ambiguous; the conflict is unrepresentable rather than
		// tie-broken.
		if seen[r.Prefix] {
			return nil, fmt.Errorf("%w: %s[%d]: duplicate prefix %q", ErrInvalid, field, i, r.Prefix)
		}
		seen[r.Prefix] = true
		rules = append(rules, r)
	}
	return rules, nil
}

func parseIdentity(n ast.Node, field string) (*IdentityRule, error) {
	m, ok := n.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be a mapping", ErrInvalid, field)
	}
	id := &IdentityRule{}
	for _, kv := range m.Values {
		key := keyString(kv.Key)
		s, ok := kv.Value.(*ast.StringNode)
		if !ok {
			return nil, fmt.Errorf("%w: %s.%s must be a string", ErrInvalid, field, key)
		}
		switch key {
		case "san":
			id.SAN = s.Value
		case "issuer":
			id.Issuer = s.Value
		default:
			return nil, fmt.Errorf("%w: %s: unknown key %q", ErrInvalid, field, key)
		}
	}
	if id.SAN == "" || id.Issuer == "" {
		return nil, fmt.Errorf("%w: %s: san and issuer are both required", ErrInvalid, field)
	}
	return id, nil
}

// Decision is the policy's verdict for one subject: whether provenance
// is required, and the explicit identity rule when one governs (nil
// leaves identity acceptance to the origin-consistency default).
type Decision struct {
	Require  bool
	Identity *IdentityRule
}

// EvaluateModule applies the modules rules to a module path
// (REQ-prov-policy-eval): the rule with the longest matching prefix
// governs, falling back to the policy default when none matches.
func (p *Policy) EvaluateModule(path string) Decision {
	return p.evaluate(p.Modules, path)
}

// EvaluatePlugin applies the plugins rules to an OCI reference. Rules
// are matched against the reference's repository part — any :tag or
// @digest stripped first — since a prefix scopes repositories, not
// versions.
func (p *Policy) EvaluatePlugin(ref string) Decision {
	return p.evaluate(p.Plugins, repositoryPart(ref))
}

// repositoryPart strips a trailing @digest and :tag from an OCI
// reference. A colon before the last "/" is a registry port, not a tag,
// and stays.
func repositoryPart(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndexByte(ref, '/')
	if colon := strings.LastIndexByte(ref, ':'); colon > slash {
		ref = ref[:colon]
	}
	return ref
}

func (p *Policy) evaluate(rules []Rule, subject string) Decision {
	var best *Rule
	for i := range rules {
		r := &rules[i]
		if !prefixMatches(r.Prefix, subject) {
			continue
		}
		if best == nil || len(r.Prefix) > len(best.Prefix) {
			best = r
		}
	}
	mode := p.Default
	var id *IdentityRule
	if best != nil {
		if best.Require != "" {
			mode = best.Require
		}
		id = best.Identity
	}
	return Decision{Require: mode == RequireProvenance, Identity: id}
}

// prefixMatches reports whether prefix governs subject at path-segment
// granularity: equal, or extended at a "/" boundary. The empty prefix
// governs everything.
func prefixMatches(prefix, subject string) bool {
	if prefix == "" {
		return true
	}
	if len(subject) < len(prefix) || subject[:len(prefix)] != prefix {
		return false
	}
	return len(subject) == len(prefix) || subject[len(prefix)] == '/'
}
