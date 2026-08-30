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
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/plugexec"
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
// package; the one home is plugexec.
const (
	TierStrong  = plugexec.TierStrong
	TierOS      = plugexec.TierOS
	TierMinimal = plugexec.TierMinimal
	TierNone    = plugexec.TierNone
	SchemeOCI   = plugexec.SchemeOCI
	SchemeLocal = plugexec.SchemeLocal
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

// Parse decodes and validates a trust policy file
// (REQ-prov-trust-schema): top-level default/modules/plugins, each
// optional; rules of shape {prefix, require, identity: {san, issuer}};
// no unknown keys anywhere.
func Parse(data []byte) (*Policy, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		// An empty or fully commented-out policy file is the empty
		// policy.
		return &Policy{}, nil
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
		case "execution":
			exec, err := parseExecution(kv.Value)
			if err != nil {
				return nil, err
			}
			p.Execution = *exec
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

func parseExecution(n ast.Node) (*Execution, error) {
	m, ok := n.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("%w: execution must be a mapping", ErrInvalid)
	}
	e := &Execution{}
	for _, kv := range m.Values {
		key := keyString(kv.Key)
		switch key {
		case "min-tier":
			s, ok := kv.Value.(*ast.StringNode)
			if !ok || !plugexec.ValidTier(s.Value) {
				return nil, fmt.Errorf("%w: execution.min-tier must be one of Strong, OS, Minimal, None", ErrInvalid)
			}
			e.MinTier = s.Value
		case "schemes":
			seq, ok := kv.Value.(*ast.SequenceNode)
			if !ok {
				return nil, fmt.Errorf("%w: execution.schemes must be a list", ErrInvalid)
			}
			schemes := make([]string, 0, len(seq.Values))
			for _, sn := range seq.Values {
				s, ok := sn.(*ast.StringNode)
				if !ok || !plugexec.ValidScheme(s.Value) {
					return nil, fmt.Errorf("%w: execution.schemes entries are oci or local", ErrInvalid)
				}
				if slices.Contains(schemes, s.Value) {
					return nil, fmt.Errorf("%w: execution.schemes lists %q twice", ErrInvalid, s.Value)
				}
				schemes = append(schemes, s.Value)
			}
			e.Schemes = schemes
		case "local-pin":
			b, err := parseBool(kv.Value, "execution.local-pin")
			if err != nil {
				return nil, err
			}
			e.LocalPin = b
		case "plugin-overrides":
			b, err := parseBool(kv.Value, "execution.plugin-overrides")
			if err != nil {
				return nil, err
			}
			e.PluginOverrides = b
		case "limits":
			limits, err := parseLimits(kv.Value)
			if err != nil {
				return nil, err
			}
			e.Limits = *limits
		default:
			return nil, fmt.Errorf("%w: execution: unknown key %q", ErrInvalid, key)
		}
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

func parseLimits(n ast.Node) (*Limits, error) {
	m, ok := n.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("%w: execution.limits must be a mapping", ErrInvalid)
	}
	l := &Limits{}
	for _, kv := range m.Values {
		key := keyString(kv.Key)
		// Every limit value is a scalar spelling validated by its own
		// grammar; typed YAML interpretations (ints, floats) are read
		// back from their source text so the grammar governs, not the
		// parser's coercion. String nodes contribute their value, not
		// their possibly-quoted rendering.
		var val string
		if s, ok := kv.Value.(*ast.StringNode); ok {
			val = s.Value
		} else {
			val = strings.TrimSpace(kv.Value.String())
		}
		var err error
		switch key {
		case "memory":
			l.Memory, err = parseMemory(val)
		case "cpu":
			l.CPU, err = parseCPU(val)
		case "pids":
			l.Pids, err = parsePositiveInt(val)
		case "timeout":
			l.Timeout, err = parseTimeout(val)
		default:
			return nil, fmt.Errorf("%w: execution.limits: unknown key %q", ErrInvalid, key)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: execution.limits.%s: %v", ErrInvalid, key, err)
		}
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
