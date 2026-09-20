// Package genfile parses and validates pb.gen.yaml — the generation
// file (generation.md REQ-gen-schema): which plugins run, in which
// identity scheme, where their output lands, and which file options
// are overridden. Parsing walks the YAML AST so the accepted surface
// is the schema's, exactly as the other contract files do, and every
// scalar is recorded with its written spelling — the parser's typed
// reading of a value is never the recorded fact.
package genfile

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/rootpath"
)

// FileName is the generation file, at the resolution root.
const FileName = "pb.gen.yaml"

// ErrInvalid marks a generation file violating the schema.
var ErrInvalid = errors.New("invalid generation file")

// Plugin is one generation entry: the plugin's identity in its scheme,
// the output directory (resolution-root-relative, forward slashes), and
// the parameter string handed to the plugin verbatim.
type Plugin struct {
	Scheme string // plugin.SchemeOCI or plugin.SchemeLocal
	Ref    string // the identity as written: an OCI reference, or a local name/path
	Out    string
	Opt    string
}

// Override is one declared file-option assignment.
type Override struct {
	Files  string // glob over module-relative proto file paths
	Option string // dotted protobuf identifier
	Value  string // the value's written spelling
}

// File is a parsed generation file.
type File struct {
	Plugins   []Plugin
	Overrides []Override
}

// Parse decodes and validates generation-file bytes (REQ-gen-schema).
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing plugins key", ErrInvalid)
	}
	f := &File{}
	seenPlugins := false
	for _, kv := range mapping.Values {
		key := keyString(kv.Key)
		switch key {
		case "plugins":
			seenPlugins = true
			plugins, err := parsePlugins(kv.Value)
			if err != nil {
				return nil, err
			}
			f.Plugins = plugins
		case "overrides":
			overrides, err := parseOverrides(kv.Value)
			if err != nil {
				return nil, err
			}
			f.Overrides = overrides
		default:
			return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, key)
		}
	}
	if !seenPlugins {
		return nil, fmt.Errorf("%w: missing plugins key", ErrInvalid)
	}
	return f, nil
}

func keyString(n ast.Node) string {
	if s, ok := n.(*ast.StringNode); ok {
		return s.Value
	}
	return n.String()
}

// scalarText returns a scalar node's written spelling: the unquoted
// value of a string node, the source token of any other scalar. A
// non-scalar (mapping, sequence, null) is not a spelling.
func scalarText(n ast.Node) (string, bool) {
	switch v := n.(type) {
	case *ast.StringNode:
		return v.Value, true
	case *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.InfinityNode, *ast.NanNode:
		return v.GetToken().Value, true
	}
	return "", false
}

func parsePlugins(n ast.Node) ([]Plugin, error) {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return nil, fmt.Errorf("%w: plugins must be a list", ErrInvalid)
	}
	if len(seq.Values) == 0 {
		return nil, fmt.Errorf("%w: plugins must not be empty", ErrInvalid)
	}
	plugins := make([]Plugin, 0, len(seq.Values))
	for i, en := range seq.Values {
		em, ok := en.(*ast.MappingNode)
		if !ok {
			return nil, fmt.Errorf("%w: plugins[%d] must be a mapping", ErrInvalid, i)
		}
		var p Plugin
		schemes := 0
		hasOut := false
		for _, kv := range em.Values {
			key := keyString(kv.Key)
			text, isScalar := scalarText(kv.Value)
			switch key {
			case "ref", "local":
				schemes++
				if !isScalar {
					return nil, fmt.Errorf("%w: plugins[%d].%s must be a string", ErrInvalid, i, key)
				}
				p.Ref = text
				p.Scheme = plugin.SchemeOCI
				if key == "local" {
					p.Scheme = plugin.SchemeLocal
				}
			case "out":
				if !isScalar {
					return nil, fmt.Errorf("%w: plugins[%d].out must be a string", ErrInvalid, i)
				}
				hasOut = true
				p.Out = text
			case "opt":
				if !isScalar {
					return nil, fmt.Errorf("%w: plugins[%d].opt must be a string", ErrInvalid, i)
				}
				p.Opt = text
			default:
				return nil, fmt.Errorf("%w: plugins[%d]: unknown key %q (entries carry ref or local, out, and optional opt)", ErrInvalid, i, key)
			}
		}
		if schemes != 1 {
			return nil, fmt.Errorf("%w: plugins[%d] must carry exactly one of ref or local, found %d", ErrInvalid, i, schemes)
		}
		if !hasOut {
			return nil, fmt.Errorf("%w: plugins[%d] has no out", ErrInvalid, i)
		}
		if err := checkIdentity(p); err != nil {
			return nil, fmt.Errorf("%w: plugins[%d]: %v", ErrInvalid, i, err)
		}
		if err := checkOut(p.Out); err != nil {
			return nil, fmt.Errorf("%w: plugins[%d].out: %v", ErrInvalid, i, err)
		}
		plugins = append(plugins, p)
	}
	return plugins, nil
}

func checkIdentity(p Plugin) error {
	switch p.Scheme {
	case plugin.SchemeOCI:
		return CheckReference(p.Ref)
	case plugin.SchemeLocal:
		return checkLocal(p.Ref)
	}
	return fmt.Errorf("unknown scheme %q", p.Scheme)
}

// ReferenceRepository returns a reference's registry/repository part —
// the tag stripped — for digest-form addressing. The reference must
// already satisfy CheckReference; the split mirrors its parsing (the
// tag is the last colon after the last slash).
func ReferenceRepository(ref string) string {
	registry, rest, _ := strings.Cut(ref, "/")
	slash := strings.LastIndexByte(rest, '/')
	if colon := strings.LastIndexByte(rest, ':'); colon > slash {
		rest = rest[:colon]
	}
	return registry + "/" + rest
}

// CheckReference validates a plugin reference (REQ-gen-schema,
// plugin-execution.md REQ-plugin-no-privileged-source):
// <registry>/<repository>:<tag>, where the registry names itself
// unambiguously (a dot, a port, or localhost), the repository is
// lowercase path components, the tag is written, and no @digest
// appears.
func CheckReference(ref string) error {
	if strings.Contains(ref, "@") {
		return fmt.Errorf("ref %q carries a digest; the lockfile pins digests", ref)
	}
	registry, rest, ok := strings.Cut(ref, "/")
	if !ok || rest == "" {
		return fmt.Errorf("ref %q has no registry: references are <registry>/<repository>:<tag>", ref)
	}
	if err := checkRegistry(registry); err != nil {
		return fmt.Errorf("ref %q: %v", ref, err)
	}
	// The tag follows the last colon after the last slash; a colon
	// before the last slash could only be a registry port, already
	// consumed above.
	slash := strings.LastIndexByte(rest, '/')
	colon := strings.LastIndexByte(rest, ':')
	if colon < 0 || colon < slash {
		return fmt.Errorf("ref %q has no tag: the tag is written, never implied", ref)
	}
	repo, tag := rest[:colon], rest[colon+1:]
	if err := checkRepository(repo); err != nil {
		return fmt.Errorf("ref %q: %v", ref, err)
	}
	if err := checkTag(tag); err != nil {
		return fmt.Errorf("ref %q: %v", ref, err)
	}
	return nil
}

// checkRegistry accepts a host — dot-joined DNS labels of lowercase
// alphanumerics with single interior hyphens, optionally with a
// :port in 1–65535 — that names itself unambiguously: it contains a
// dot or a port, or is localhost. A bare name like "library" is a
// short name, which no privileged expansion exists for.
func checkRegistry(s string) error {
	host, port, hasPort := strings.Cut(s, ":")
	// An empty host splits to one empty label, which the label grammar
	// rejects; no separate emptiness guard.
	for _, label := range strings.Split(host, ".") {
		if err := checkLabel(label); err != nil {
			return fmt.Errorf("registry %q: %v", s, err)
		}
	}
	if hasPort {
		if port == "" {
			return fmt.Errorf("registry %q has an empty port", s)
		}
		n := 0
		for i := 0; i < len(port); i++ {
			if port[i] < '0' || port[i] > '9' {
				return fmt.Errorf("registry %q has a non-numeric port", s)
			}
			n = n*10 + int(port[i]-'0')
			if n > 65535 {
				return fmt.Errorf("registry %q has a port beyond 65535", s)
			}
		}
		if n == 0 {
			return fmt.Errorf("registry %q has port 0", s)
		}
	}
	if !hasPort && host != "localhost" && !strings.Contains(host, ".") {
		return fmt.Errorf("registry %q does not name itself unambiguously (no dot, no port, not localhost): there is no default registry", s)
	}
	return nil
}

// checkLabel accepts one DNS label: lowercase alphanumerics with
// single interior hyphens.
func checkLabel(l string) error {
	alnum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
	if l == "" || !alnum(l[0]) || !alnum(l[len(l)-1]) {
		return fmt.Errorf("label %q must be non-empty and start and end alphanumeric", l)
	}
	for i := 0; i < len(l); i++ {
		if !(alnum(l[i]) || l[i] == '-') {
			return fmt.Errorf("label %q is not lowercase alphanumerics with single hyphens", l)
		}
	}
	if strings.Contains(l, "--") {
		return fmt.Errorf("label %q is not lowercase alphanumerics with single hyphens", l)
	}
	return nil
}

// checkRepository accepts one or more lowercase components joined by
// "/", each following the OCI distribution grammar: alphanumerics
// with separators strictly inside — a single ".", one or more "-", or
// one or two "_".
func checkRepository(s string) error {
	// An empty repository splits to one empty component, which the
	// component grammar rejects; no separate emptiness guard.
	for _, comp := range strings.Split(s, "/") {
		if err := checkRepoComponent(comp); err != nil {
			return err
		}
	}
	return nil
}

func checkRepoComponent(c string) error {
	if c == "" {
		return errors.New("empty repository component")
	}
	alnum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
	if !alnum(c[0]) || !alnum(c[len(c)-1]) {
		return fmt.Errorf("repository component %q must start and end alphanumeric", c)
	}
	// One scan with a separator-run state: a run is one separator kind
	// repeated — "." once, "-" any length, "_" at most twice — bounded
	// by alphanumerics; the start/end check above bounds the outer
	// edges, so only adjacency between different kinds and run length
	// remain to police here.
	var prev byte
	run := 0
	for i := 0; i < len(c); i++ {
		b := c[i]
		if alnum(b) {
			prev, run = 0, 0
			continue
		}
		if b != '.' && b != '-' && b != '_' {
			return fmt.Errorf("repository component %q contains %q", c, b)
		}
		if prev != 0 && prev != b {
			return fmt.Errorf("repository component %q has adjacent separators", c)
		}
		run++
		if (b == '.' && run > 1) || (b == '_' && run > 2) {
			return fmt.Errorf("repository component %q has an invalid separator run", c)
		}
		prev = b
	}
	return nil
}

// checkTag accepts up to 128 characters of [A-Za-z0-9_.-], starting
// alphanumeric or underscore.
func checkTag(t string) error {
	if t == "" || len(t) > 128 {
		return fmt.Errorf("tag %q is empty or longer than 128 characters", t)
	}
	// Every byte is validated by the loop; the first additionally may
	// not be one of the two loop-legal separators.
	if t[0] == '.' || t[0] == '-' {
		return fmt.Errorf("tag %q must start with a letter, digit, or underscore", t)
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return fmt.Errorf("tag %q contains %q", t, c)
		}
	}
	return nil
}

// checkLocal accepts a local plugin value (plugin-execution.md
// REQ-plugin-local-resolution): a bare program name, or a forward-slash
// path — relative or absolute. Backslashes are never separators.
func checkLocal(s string) error {
	if s == "" {
		return errors.New("empty local value")
	}
	if strings.ContainsRune(s, '\\') {
		return fmt.Errorf("local %q: paths are written with forward slashes", s)
	}
	// A leading "./" is the idiomatic spelling of "this path, not a
	// PATH lookup"; the rest must be clean. A bare name is trivially
	// clean, so one rule covers both forms.
	rest := strings.TrimPrefix(s, "./")
	// path.Clean("") is ".", so an empty remainder fails this one check.
	if path.Clean(rest) != rest {
		return fmt.Errorf("local %q is not a clean path", s)
	}
	if base := path.Base(rest); base == "." || base == ".." || base == "/" {
		return fmt.Errorf("local %q names no program", s)
	}
	return nil
}

// checkOut accepts a clean relative forward-slash path that never
// escapes the resolution root: the written-spelling rule of
// committed configuration (backslashes are never separators) over the
// shared containment judgment.
func checkOut(s string) error {
	if strings.ContainsRune(s, '\\') {
		return errors.New("paths are written with forward slashes")
	}
	return rootpath.Check(s, "the resolution root")
}

func parseOverrides(n ast.Node) ([]Override, error) {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return nil, fmt.Errorf("%w: overrides must be a list", ErrInvalid)
	}
	overrides := make([]Override, 0, len(seq.Values))
	for i, en := range seq.Values {
		em, ok := en.(*ast.MappingNode)
		if !ok {
			return nil, fmt.Errorf("%w: overrides[%d] must be a mapping", ErrInvalid, i)
		}
		var o Override
		seen := map[string]bool{}
		for _, kv := range em.Values {
			key := keyString(kv.Key)
			text, isScalar := scalarText(kv.Value)
			switch key {
			case "files", "option", "value":
			default:
				return nil, fmt.Errorf("%w: overrides[%d]: unknown key %q", ErrInvalid, i, key)
			}
			if !isScalar {
				return nil, fmt.Errorf("%w: overrides[%d].%s must be a string", ErrInvalid, i, key)
			}
			seen[key] = true
			switch key {
			case "files":
				o.Files = text
			case "option":
				o.Option = text
			case "value":
				o.Value = text
			}
		}
		for _, required := range []string{"files", "option", "value"} {
			if !seen[required] {
				return nil, fmt.Errorf("%w: overrides[%d] has no %s", ErrInvalid, i, required)
			}
		}
		if _, err := glob.Compile(o.Files); err != nil {
			return nil, fmt.Errorf("%w: overrides[%d].files: %v", ErrInvalid, i, err)
		}
		if err := checkOptionName(o.Option); err != nil {
			return nil, fmt.Errorf("%w: overrides[%d].option: %v", ErrInvalid, i, err)
		}
		overrides = append(overrides, o)
	}
	return overrides, nil
}

// SplitOption parses a protobuf option name into its one navigable
// shape: a built-in field name, or an extension's fully-qualified name
// with an optional one-level field selector. It is the single home of
// the option-name grammar — validation and navigation share it, so a
// spelling that validates always navigates.
func SplitOption(s string) (extension, field, builtin string, err error) {
	if s == "" {
		return "", "", "", errors.New("empty")
	}
	if s[0] != '(' {
		if err := checkDottedIdent(s, s); err != nil {
			return "", "", "", err
		}
		return "", "", s, nil
	}
	end := strings.IndexByte(s, ')')
	if end < 0 {
		return "", "", "", fmt.Errorf("%q has an unclosed extension name", s)
	}
	extension = s[1:end]
	if !qualifiedIdent(extension) {
		return "", "", "", fmt.Errorf("%q has an invalid extension name", s)
	}
	rest := s[end+1:]
	if rest == "" {
		return extension, "", "", nil
	}
	if rest[0] != '.' || len(rest) == 1 {
		return "", "", "", fmt.Errorf("%q is not a protobuf option name", s)
	}
	field = rest[1:]
	if err := checkDottedIdent(field, s); err != nil {
		return "", "", "", err
	}
	return extension, field, "", nil
}

// checkOptionName accepts what SplitOption parses.
func checkOptionName(s string) error {
	_, _, _, err := SplitOption(s)
	return err
}

func ident(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		if !(letter || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func qualifiedIdent(q string) bool {
	for _, id := range strings.Split(q, ".") {
		if !ident(id) {
			return false
		}
	}
	return true
}

// checkDottedIdent accepts dot-joined identifiers, reporting against
// the whole written name.
func checkDottedIdent(s, whole string) error {
	if !qualifiedIdent(s) {
		return fmt.Errorf("%q is not a protobuf option name", whole)
	}
	return nil
}
