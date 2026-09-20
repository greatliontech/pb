// Package trust parses and evaluates the trust policy file
// (pb.trust.yaml) — the resolution root's statement of which provenance
// evidence is required and which identities are accepted, for modules
// and plugin images alike (REQ-prov-trust-schema, REQ-prov-policy-eval).
// Parsing goes through the YAML AST so the accepted surface is the
// schema's, not the parser's, exactly as the other contract files do.
// The file's execution block is this package's too: the sandbox tier
// floor, the resource bounds, which identity schemes and overrides
// a root admits (REQ-prov-exec-policy) — the policy the runners are
// held to, evaluated here beside the provenance policy because one
// file states both.
//
// Prefix matching is path-segment-aware: a rule prefix matches a
// subject that equals it or extends it at a "/" boundary — a plain
// string prefix would let "host/org" govern "host/organization", a
// scope the rule's author never granted.
package trust

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/plugin"
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

// Vocabulary re-exported for callers already naming it through this
// package; the one home is plugin.
const (
	TierStrong  = plugin.TierStrong
	TierOS      = plugin.TierOS
	TierMinimal = plugin.TierMinimal
	TierNone    = plugin.TierNone
	SchemeOCI   = plugin.SchemeOCI
	SchemeLocal = plugin.SchemeLocal
)

// Limits are plugin resource bounds overriding the implementation
// defaults (REQ-prov-exec-policy); a zero field means the default. The
// value grammars admit no zero spelling, so zero is unambiguously
// "unset".
type Limits struct {
	Memory  uint64        // bytes
	CPU     float64       // cores
	Pids    uint64        // processes
	Timeout time.Duration // wall clock
}

// Implementation-declared resource-bound defaults
// (REQ-plugin-resource-bounds), folded into unset limit fields by
// EffectiveLimits. The CPU default is the host's CPU count — "no more
// than the machine" — declared explicitly rather than implied.
const (
	DefaultMemoryBytes uint64 = 2 << 30
	DefaultPids        uint64 = 512
	DefaultTimeout            = 5 * time.Minute
)

// Execution is the resolution root's plugin-execution posture
// (REQ-prov-exec-policy). Nil-able fields distinguish "unset" from an
// explicit choice; the Effective* / *Allowed accessors fold in the
// defaults, and are the only sanctioned readers.
type Execution struct {
	MinTier         string   // "" = Strong
	Schemes         []string // nil = [oci]
	LocalPin        *bool    // nil = true
	PluginOverrides *bool    // nil = true
	Limits          Limits
}

// EffectiveMinTier is the sandbox tier floor for oci plugins.
func (e *Execution) EffectiveMinTier() string {
	if e.MinTier == "" {
		return TierStrong
	}
	return e.MinTier
}

// EffectiveLimits folds the declared defaults into unset limit fields;
// every field of the result is set, so a runner never sees an
// unbounded resource.
func (e *Execution) EffectiveLimits() Limits {
	l := e.Limits
	if l.Memory == 0 {
		l.Memory = DefaultMemoryBytes
	}
	if l.CPU == 0 {
		l.CPU = float64(runtime.NumCPU())
	}
	if l.Pids == 0 {
		l.Pids = DefaultPids
	}
	if l.Timeout == 0 {
		l.Timeout = DefaultTimeout
	}
	return l
}

// SchemeAllowed reports whether the posture permits an identity scheme.
func (e *Execution) SchemeAllowed(scheme string) bool {
	if e.Schemes == nil {
		return scheme == SchemeOCI
	}
	return slices.Contains(e.Schemes, scheme)
}

// LocalPinEnabled reports whether local binaries are content-hash
// pinned.
func (e *Execution) LocalPinEnabled() bool {
	return e.LocalPin == nil || *e.LocalPin
}

// OverridesAllowed reports whether invocation-scoped plugin overrides
// are permitted.
func (e *Execution) OverridesAllowed() bool {
	return e.PluginOverrides == nil || *e.PluginOverrides
}

// Policy is a parsed trust policy. The zero value is the empty policy:
// allow-unsigned for everything, no identity rules, the default
// execution posture.
type Policy struct {
	Default   Mode // "" = AllowUnsigned
	Modules   []Rule
	Plugins   []Rule
	Execution Execution
}

// ErrInvalid marks a trust policy violating the schema.
var ErrInvalid = errors.New("invalid trust policy")

// Parse decodes and validates a trust policy (REQ-prov-trust-schema,
// REQ-prov-exec-policy): the document a mapping of default, modules,
// plugins and execution, each optional; an empty document the empty
// policy.
func Parse(data []byte) (*Policy, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	p := &Policy{}
	if mapping == nil {
		// An empty or fully commented-out policy file is the empty
		// policy.
		return p, nil
	}
	rules := func(field string, into *[]Rule) contractfile.Field {
		return contractfile.Field{Name: field, Read: func(n ast.Node) error {
			rs, err := parseRules(n, field)
			*into = rs
			return err
		}}
	}
	err = contractfile.Mapping(mapping, "", ErrInvalid,
		contractfile.Field{Name: "default", Read: func(n ast.Node) error {
			m, err := parseMode(n, "default")
			p.Default = m
			return err
		}},
		rules("modules", &p.Modules),
		rules("plugins", &p.Plugins),
		contractfile.Field{Name: "execution", Read: func(n ast.Node) error {
			exec, err := parseExecution(n)
			if err != nil {
				return err
			}
			p.Execution = *exec
			return nil
		}},
	)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func parseMode(n ast.Node, field string) (Mode, error) {
	s, ok := contractfile.Line(n)
	if !ok {
		return "", fmt.Errorf("%w: %s must be one line of text", ErrInvalid, field)
	}
	m := Mode(s)
	if m != AllowUnsigned && m != RequireProvenance {
		return "", fmt.Errorf("%w: %s: unknown mode %q", ErrInvalid, field, s)
	}
	return m, nil
}

// parseRules reads a rules list: each entry prefix, require and
// identity, prefixes unique within the list.
func parseRules(n ast.Node, field string) ([]Rule, error) {
	seen := map[string]bool{}
	rules := []Rule{}
	err := contractfile.Sequence(n, field, ErrInvalid, func(i int, rn ast.Node) error {
		where := fmt.Sprintf("%s[%d]", field, i)
		var r Rule
		err := contractfile.Mapping(rn, where, ErrInvalid,
			contractfile.Field{Name: "prefix", Read: func(n ast.Node) error {
				s, ok := contractfile.Line(n)
				if !ok {
					return fmt.Errorf("%w: %s.prefix must be one line of text", ErrInvalid, where)
				}
				r.Prefix = s
				return nil
			}},
			contractfile.Field{Name: "require", Read: func(n ast.Node) error {
				m, err := parseMode(n, where+".require")
				r.Require = m
				return err
			}},
			contractfile.Field{Name: "identity", Read: func(n ast.Node) error {
				id, err := parseIdentity(n, where+".identity")
				r.Identity = id
				return err
			}},
		)
		if err != nil {
			return err
		}
		// A duplicate prefix would make "the longest matching rule"
		// ambiguous; the conflict is unrepresentable rather than
		// tie-broken.
		if seen[r.Prefix] {
			return fmt.Errorf("%w: %s: duplicate prefix %q", ErrInvalid, where, r.Prefix)
		}
		seen[r.Prefix] = true
		rules = append(rules, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// parseExecution reads the execution block (REQ-prov-exec-policy).
func parseExecution(n ast.Node) (*Execution, error) {
	e := &Execution{}
	boolean := func(name string, into **bool) contractfile.Field {
		return contractfile.Field{Name: name, Read: func(n ast.Node) error {
			b, err := parseBool(n, "execution."+name)
			*into = b
			return err
		}}
	}
	err := contractfile.Mapping(n, "execution", ErrInvalid,
		contractfile.Field{Name: "min-tier", Read: func(n ast.Node) error {
			s, ok := contractfile.Line(n)
			if !ok || !plugin.ValidTier(s) {
				return fmt.Errorf("%w: execution.min-tier must be one of Strong, OS, Minimal, None", ErrInvalid)
			}
			e.MinTier = s
			return nil
		}},
		contractfile.Field{Name: "schemes", Read: func(n ast.Node) error {
			schemes := []string{}
			err := contractfile.Sequence(n, "execution.schemes", ErrInvalid, func(_ int, sn ast.Node) error {
				s, ok := contractfile.Line(sn)
				if !ok || !plugin.ValidScheme(s) {
					return fmt.Errorf("%w: execution.schemes entries are oci or local", ErrInvalid)
				}
				if slices.Contains(schemes, s) {
					return fmt.Errorf("%w: execution.schemes lists %q twice", ErrInvalid, s)
				}
				schemes = append(schemes, s)
				return nil
			})
			e.Schemes = schemes
			return err
		}},
		boolean("local-pin", &e.LocalPin),
		boolean("plugin-overrides", &e.PluginOverrides),
		contractfile.Field{Name: "limits", Read: func(n ast.Node) error {
			limits, err := parseLimits(n)
			if err != nil {
				return err
			}
			e.Limits = *limits
			return nil
		}},
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func parseBool(n ast.Node, field string) (*bool, error) {
	b, ok := n.(*ast.BoolNode)
	// The token text pins the spelling: YAML's capitalized boolean
	// spellings decode to the same value, but the schema admits exactly
	// the lowercase pair, consistent with the limit grammars' strictness.
	if !ok || (b.GetToken().Value != "true" && b.GetToken().Value != "false") {
		return nil, fmt.Errorf("%w: %s must be true or false", ErrInvalid, field)
	}
	v := b.Value
	return &v, nil
}

// parseLimits reads the resource limits: every value a scalar
// spelling validated by its own grammar — the text written, never the
// parser's coercion.
func parseLimits(n ast.Node) (*Limits, error) {
	l := &Limits{}
	limit := func(name string, set func(string) error) contractfile.Field {
		return contractfile.Field{Name: name, Read: func(n ast.Node) error {
			val, ok := contractfile.Line(n)
			if !ok {
				return fmt.Errorf("%w: execution.limits.%s must be one line of text", ErrInvalid, name)
			}
			if err := set(val); err != nil {
				return fmt.Errorf("%w: execution.limits.%s: %v", ErrInvalid, name, err)
			}
			return nil
		}}
	}
	err := contractfile.Mapping(n, "execution.limits", ErrInvalid,
		limit("memory", func(v string) (err error) { l.Memory, err = parseMemory(v); return }),
		limit("cpu", func(v string) (err error) { l.CPU, err = parseCPU(v); return }),
		limit("pids", func(v string) (err error) { l.Pids, err = parsePositiveInt(v); return }),
		limit("timeout", func(v string) (err error) { l.Timeout, err = parseTimeout(v); return }),
	)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// parsePositiveInt accepts decimal digits naming a positive integer.
func parsePositiveInt(s string) (uint64, error) {
	if s == "" {
		return 0, errors.New("empty value")
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%q is not a positive integer", s)
		}
		d := uint64(c - '0')
		if v > (^uint64(0)-d)/10 {
			return 0, fmt.Errorf("%q overflows", s)
		}
		v = v*10 + d
	}
	if v == 0 {
		return 0, fmt.Errorf("%q is not positive", s)
	}
	return v, nil
}

// parseMemory accepts a positive integer byte count with optional
// Ki/Mi/Gi suffix.
func parseMemory(s string) (uint64, error) {
	mult := uint64(1)
	for _, suf := range []struct {
		name string
		mult uint64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}} {
		if rest, ok := strings.CutSuffix(s, suf.name); ok {
			s, mult = rest, suf.mult
			break
		}
	}
	v, err := parsePositiveInt(s)
	if err != nil {
		return 0, err
	}
	if v > ^uint64(0)/mult {
		return 0, fmt.Errorf("%q overflows", s)
	}
	return v * mult, nil
}

// parseDecimal accepts a positive plain decimal: digits with one
// optional fractional part. No exponents, signs, or spellings a float
// parser would additionally admit.
func parseDecimal(s string) (float64, error) {
	whole, frac, hasFrac := strings.Cut(s, ".")
	digits := func(d string) bool {
		for i := 0; i < len(d); i++ {
			if d[i] < '0' || d[i] > '9' {
				return false
			}
		}
		return len(d) > 0
	}
	if !digits(whole) || (hasFrac && !digits(frac)) {
		return 0, fmt.Errorf("%q is not a plain decimal", s)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%q is not a positive decimal", s)
	}
	return v, nil
}

// parseCPU accepts a positive decimal core count.
func parseCPU(s string) (float64, error) {
	return parseDecimal(s)
}

// parseTimeout accepts a positive decimal with unit s, m, or h.
func parseTimeout(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty value")
	}
	unit := time.Duration(0)
	switch s[len(s)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	default:
		return 0, fmt.Errorf("%q has no s/m/h unit", s)
	}
	v, err := parseDecimal(s[:len(s)-1])
	if err != nil {
		return 0, err
	}
	// Duration is an int64 nanosecond count: a value past its range
	// would wrap negative in the conversion, turning a "positive
	// decimal" spelling into a negative bound.
	if v > float64(math.MaxInt64)/float64(unit) {
		return 0, fmt.Errorf("%q overflows", s)
	}
	return time.Duration(v * float64(unit)), nil
}

// parseIdentity reads an identity rule: san and issuer, both
// required, each one line of text.
func parseIdentity(n ast.Node, field string) (*IdentityRule, error) {
	id := &IdentityRule{}
	line := func(name string, into *string) contractfile.Field {
		return contractfile.Field{Name: name, Required: true, Read: func(n ast.Node) error {
			s, ok := contractfile.Line(n)
			if !ok || s == "" {
				return fmt.Errorf("%w: %s.%s must be one non-empty line of text", ErrInvalid, field, name)
			}
			*into = s
			return nil
		}}
	}
	if err := contractfile.Mapping(n, field, ErrInvalid, line("san", &id.SAN), line("issuer", &id.Issuer)); err != nil {
		return nil, err
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
