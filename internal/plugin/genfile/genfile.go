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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/module"
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
	Ref    string // the identity as written: an OCI reference, or a local command
	// Args are a local command's arguments, handed to the process
	// verbatim after it (REQ-gen-schema's list form); none for an
	// oci entry.
	Args []string
	Out  string
	Opt  string
	// Files are the glob patterns selecting the entry's targets among
	// the workspace's module-relative paths (REQ-gen-request); none
	// selects every workspace file.
	Files []string
	// IncludeImports adds the files the targets reach through imports.
	IncludeImports bool
	// IncludeWKT adds the well-known imports among those, admitted
	// beside IncludeImports alone (REQ-gen-schema).
	IncludeWKT bool
	// Clean empties the entry's output directory before any plugin
	// runs (REQ-gen-clean).
	Clean bool
}

// Command is the entry's plugin as the entry spells it: the reference,
// or the local command with its arguments after it, space-separated
// — the name generation's report and refusals call the entry by.
func (p Plugin) Command() string {
	if len(p.Args) == 0 {
		return p.Ref
	}
	return p.Ref + " " + strings.Join(p.Args, " ")
}

// Equal reports whether two entries are the same entry, argument for
// argument.
func (p Plugin) Equal(q Plugin) bool {
	return p.Scheme == q.Scheme && p.Ref == q.Ref && p.Out == q.Out && p.Opt == q.Opt && slices.Equal(p.Args, q.Args) &&
		slices.Equal(p.Files, q.Files) && p.IncludeImports == q.IncludeImports && p.IncludeWKT == q.IncludeWKT && p.Clean == q.Clean
}

// Override is one declared file-option assignment: a value, or a
// derivation from a prefix and/or suffix, or from the file alone
// (REQ-gen-overrides-derived), over the files its glob matches,
// narrowed to one module's or leaving some modules' out
// (REQ-gen-schema).
type Override struct {
	Files  string   // glob over module-relative proto file paths
	Module string   // the one module whose files the entry covers; "" for every module
	Except []string // the modules whose files the entry leaves out; none with Module
	Option string   // dotted protobuf identifier
	Value  string   // the value's written spelling; "" for a derived override
	Prefix string   // a derived override's prefix
	Suffix string   // a derived override's suffix
	// Bare marks a derivation from the file alone: no prefix, no
	// suffix, the rule spelling the value from the file's facts.
	Bare bool
}

// Derived reports whether the override derives its value.
func (o Override) Derived() bool { return o.Prefix != "" || o.Suffix != "" || o.Bare }

// Equal reports whether two overrides are the same override, module
// for module.
func (o Override) Equal(q Override) bool {
	return o.Files == q.Files && o.Module == q.Module && slices.Equal(o.Except, q.Except) && o.Option == q.Option &&
		o.Value == q.Value && o.Prefix == q.Prefix && o.Suffix == q.Suffix && o.Bare == q.Bare
}

// Scoped reports whether the override covers a file the module at
// path provides: every module's where it names none, the one named,
// or any but those excepted.
func (o Override) Scoped(modulePath string) bool {
	if o.Module != "" {
		return modulePath == o.Module
	}
	return !slices.Contains(o.Except, modulePath)
}

// derivable is the derivation rule of each option a derived override
// may name (REQ-gen-overrides-derived): which of prefix and suffix
// it reads, whether it stands with neither (bare), and the value it
// spells for one file from its module-relative path and its
// package, false where the file declares no package and the rule
// reads it.
var derivable = map[string]struct {
	prefix, suffix, bare bool
	rule                 func(o Override, filePath, pkg string) (string, bool)
}{
	"go_package": {prefix: true, rule: func(o Override, filePath, pkg string) (string, bool) {
		v := path.Join(o.Prefix, path.Dir(filePath))
		parts := strings.Split(pkg, ".")
		if n := len(parts); n >= 2 && isPackageVersion(parts[n-1]) {
			v += ";" + parts[n-2] + parts[n-1]
		}
		return v, true
	}},
	"java_package": {prefix: true, suffix: true, rule: func(o Override, _, pkg string) (string, bool) {
		if pkg == "" {
			return "", false
		}
		v := pkg
		if o.Prefix != "" {
			v = o.Prefix + "." + v
		}
		if o.Suffix != "" {
			v += "." + o.Suffix
		}
		return v, true
	}},
	"csharp_namespace": {prefix: true, bare: true, rule: func(o Override, _, pkg string) (string, bool) {
		if pkg == "" {
			return "", false
		}
		v := strings.Join(pascal(pkg, false), ".")
		if o.Prefix != "" {
			v = o.Prefix + "." + v
		}
		return v, true
	}},
	"php_namespace": {bare: true, rule: func(_ Override, _, pkg string) (string, bool) {
		return phpNamespace(pkg)
	}},
	"php_metadata_namespace": {suffix: true, bare: true, rule: func(o Override, _, pkg string) (string, bool) {
		ns, ok := phpNamespace(pkg)
		if !ok {
			return "", false
		}
		suffix := o.Suffix
		if suffix == "" {
			suffix = "GPBMetadata"
		}
		return ns + `\` + suffix, true
	}},
	"ruby_package": {suffix: true, bare: true, rule: func(o Override, _, pkg string) (string, bool) {
		if pkg == "" {
			return "", false
		}
		v := strings.Join(pascal(pkg, false), "::")
		if o.Suffix != "" {
			v += "::" + o.Suffix
		}
		return v, true
	}},
	"java_outer_classname": {bare: true, rule: func(_ Override, filePath, _ string) (string, bool) {
		return pascalWords(path.Base(filePath), ".-_ "), true
	}},
	"objc_class_prefix": {bare: true, rule: func(_ Override, _, pkg string) (string, bool) {
		if pkg == "" {
			return "", false
		}
		parts := strings.Split(pkg, ".")
		if n := len(parts); n >= 2 && isPackageVersion(parts[n-1]) {
			parts = parts[:n-1]
		}
		var letters []rune
		for _, part := range parts {
			letters = append(letters, unicode.ToUpper([]rune(part)[0]))
		}
		for len(letters) < 3 {
			letters = append(letters, 'X')
		}
		if string(letters) == "GPB" {
			return "GPX", true
		}
		return string(letters), true
	}},
}

// CheckDerivation refuses a derived override naming an option no rule
// derives, a prefix or suffix its rule does not read, or neither
// where its rule reads one.
func CheckDerivation(o Override) error {
	d, ok := derivable[o.Option]
	if !ok {
		return fmt.Errorf("option %s has no derivation rule; write its value", o.Option)
	}
	if o.Prefix != "" && !d.prefix {
		return fmt.Errorf("option %s derives from no prefix", o.Option)
	}
	if o.Suffix != "" && !d.suffix {
		return fmt.Errorf("option %s derives from no suffix", o.Option)
	}
	if o.Prefix == "" && o.Suffix == "" && !d.bare {
		axes := "a prefix"
		if d.suffix {
			axes = "a prefix or a suffix"
		}
		return fmt.Errorf("option %s derives from %s, and none is written", o.Option, axes)
	}
	return nil
}

// Derive spells a derived override's value for one file from its
// module-relative path and its package (REQ-gen-overrides-derived);
// false where the rule reads the package and the file declares none,
// or the override derives nothing, or is no derivation its option's
// rule admits (CheckDerivation).
func (o Override) Derive(filePath, pkg string) (string, bool) {
	d, ok := derivable[o.Option]
	if !ok || !o.Derived() || CheckDerivation(o) != nil {
		return "", false
	}
	return d.rule(o, filePath, pkg)
}

// packageVersion matches a package component that is a version
// (REQ-gen-overrides-derived): `v` and a number, then nothing, `test`
// and anything, or an optional `p` and a number followed by `alpha`
// or `beta` and an optional number; its numbers are the groups.
var packageVersion = regexp.MustCompile(`^v(0*[1-9][0-9]*)(?:test.*|(?:p(0*[1-9][0-9]*))?(?:alpha|beta)(0*[1-9][0-9]*)?)?$`)

// isPackageVersion reports whether a package component is a version:
// its form, each number worth at least one and at most 2147483647.
func isPackageVersion(component string) bool {
	m := packageVersion.FindStringSubmatch(component)
	if m == nil {
		return false
	}
	for _, n := range m[1:] {
		if n == "" {
			continue
		}
		if _, err := strconv.ParseInt(n, 10, 32); err != nil {
			return false
		}
	}
	return true
}

// pascal is a package's components each in PascalCase: its
// underscores dropped and the letter after each, and its first,
// upper-cased, the rest as written; with php, a component that is a
// PHP reserved word gets `_` appended.
func pascal(pkg string, php bool) []string {
	parts := strings.Split(pkg, ".")
	for i, part := range parts {
		parts[i] = pascalWords(part, "_")
		if php && phpReserved[strings.ToLower(part)] {
			parts[i] += "_"
		}
	}
	return parts
}

// pascalWords is text in PascalCase: split at any of the separators,
// each piece's first character upper-cased, the rest as written, the
// separators dropped.
func pascalWords(text, separators string) string {
	var b strings.Builder
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune(separators, r) }) {
		first, size := utf8.DecodeRuneInString(word)
		b.WriteRune(unicode.ToUpper(first))
		b.WriteString(word[size:])
	}
	return b.String()
}

// phpNamespace is the php_namespace rule's value for a package
// (REQ-gen-overrides-derived): its components in PascalCase, a
// reserved word's `_` appended, `\`-joined; false for no package.
func phpNamespace(pkg string) (string, bool) {
	if pkg == "" {
		return "", false
	}
	return strings.Join(pascal(pkg, true), `\`), true
}

// phpReserved is PHP's reserved words and predefined class names,
// which a PHP namespace component may not be (REQ-gen-overrides-derived).
var phpReserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`directory exception errorexception closure generator arithmeticerror
		assertionerror divisionbyzeroerror error throwable parseerror typeerror abstract and array as
		break callable case catch class clone const continue declare default die do echo else elseif
		empty enddeclare endfor endforeach endif endswitch endwhile eval exit extends final finally fn
		for foreach function global goto if implements include include_once instanceof insteadof
		interface isset list match namespace new or print private protected public require
		require_once return static switch throw trait try unset use var while xor yield int float
		bool string true false null void iterable`) {
		phpReserved[w] = true
	}
}

// File is a parsed generation file.
type File struct {
	Plugins   []Plugin
	Overrides []Override
	// Clean empties every entry's output directory before any
	// plugin runs, as an entry's own Clean empties its (REQ-gen-clean).
	Clean bool
}

// Parse decodes and validates a generate file (REQ-gen-schema): the
// document a mapping of plugins, required, and overrides.
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing plugins", ErrInvalid)
	}
	f := &File{}
	err = contractfile.Mapping(mapping, "", ErrInvalid,
		boolField("", "clean", &f.Clean),
		contractfile.Field{Name: "plugins", Required: true, Read: func(n ast.Node) error {
			plugins, err := parsePlugins(n)
			f.Plugins = plugins
			return err
		}},
		contractfile.Field{Name: "overrides", Read: func(n ast.Node) error {
			overrides, err := parseOverrides(n)
			f.Overrides = overrides
			return err
		}},
	)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Encode renders the file canonically (REQ-gen-emission): clean where
// true, plugins, then overrides where any, entries in the order given,
// an entry's keys in the order ref or local, out, opt — absent where
// empty — files (absent where every file is a target),
// include_imports, include_wkt and clean (each absent where false),
// and an override's files,
// option, value, each scalar spelled as contractfile.Spell has it; a
// local with arguments is a block sequence, the command first, one
// without the scalar. The rendering is held to its reading —
// Encode never emits what Parse rejects, nor what Parse reads as a
// different file.
func Encode(f *File) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: no file", ErrInvalid)
	}
	return contractfile.Emit(func(w *contractfile.Writer) {
		if f.Clean {
			w.Literal("clean", "true")
		}
		w.Sequence("plugins", len(f.Plugins), func(i int) {
			p := f.Plugins[i]
			switch {
			case p.Scheme == plugin.SchemeLocal && len(p.Args) > 0:
				w.List("local", append([]string{p.Ref}, p.Args...))
			case p.Scheme == plugin.SchemeLocal:
				w.Scalar("local", p.Ref)
			default:
				w.Scalar("ref", p.Ref)
			}
			w.Scalar("out", p.Out)
			if p.Opt != "" {
				w.Scalar("opt", p.Opt)
			}
			if len(p.Files) > 0 {
				w.List("files", p.Files)
			}
			if p.IncludeImports {
				w.Literal("include_imports", "true")
			}
			if p.IncludeWKT {
				w.Literal("include_wkt", "true")
			}
			if p.Clean {
				w.Literal("clean", "true")
			}
		})
		if len(f.Overrides) > 0 {
			w.Sequence("overrides", len(f.Overrides), func(i int) {
				o := f.Overrides[i]
				w.Scalar("files", o.Files)
				if o.Module != "" {
					w.Scalar("module", o.Module)
				}
				if len(o.Except) > 0 {
					w.List("except", o.Except)
				}
				w.Scalar("option", o.Option)
				if o.Derived() {
					if o.Prefix != "" {
						w.Scalar("prefix", o.Prefix)
					}
					if o.Suffix != "" {
						w.Scalar("suffix", o.Suffix)
					}
				} else {
					w.Scalar("value", o.Value)
				}
			})
		}
	}, Parse, f, func(a, b *File) bool {
		return a.Clean == b.Clean && slices.EqualFunc(a.Plugins, b.Plugins, Plugin.Equal) && slices.EqualFunc(a.Overrides, b.Overrides, Override.Equal)
	}, ErrInvalid)
}

// parsePlugins reads the plugins list: each entry a mapping carrying
// exactly one of ref or local, out, and optionally opt — a reference
// one line of text, a local one line or a list of them, the command
// then its arguments, a list of one the same entry as its scalar,
// the parameter string text as written.
func parsePlugins(n ast.Node) ([]Plugin, error) {
	plugins := []Plugin{}
	err := contractfile.Sequence(n, "plugins", ErrInvalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("plugins[%d]", i)
		var p Plugin
		schemes := 0
		hasOut := false
		line := func(name string, set func(string)) contractfile.Field {
			return contractfile.Field{Name: name, Read: func(n ast.Node) error {
				text, ok := contractfile.Line(n)
				if !ok {
					return fmt.Errorf("%w: %s.%s must be one line of text", ErrInvalid, where, name)
				}
				set(text)
				return nil
			}}
		}
		err := contractfile.Mapping(en, where, ErrInvalid,
			line("ref", func(s string) { schemes++; p.Ref, p.Scheme = s, plugin.SchemeOCI }),
			contractfile.Field{Name: "local", Read: func(n ast.Node) error {
				schemes++
				p.Scheme = plugin.SchemeLocal
				if text, ok := contractfile.Line(n); ok {
					p.Ref = text
					return nil
				}
				if _, isList := n.(*ast.SequenceNode); !isList {
					return fmt.Errorf("%w: %s.local must be one line of text or a list of them", ErrInvalid, where)
				}
				var argv []string
				err := contractfile.Sequence(n, where+".local", ErrInvalid, func(i int, item ast.Node) error {
					text, ok := contractfile.Line(item)
					if !ok {
						return fmt.Errorf("%w: %s.local[%d] must be one line of text", ErrInvalid, where, i)
					}
					argv = append(argv, text)
					return nil
				})
				if err != nil {
					return err
				}
				if len(argv) == 0 {
					return fmt.Errorf("%w: %s.local names no command", ErrInvalid, where)
				}
				p.Ref = argv[0]
				if len(argv) > 1 {
					p.Args = argv[1:]
				}
				return nil
			}},
			line("out", func(s string) { hasOut = true; p.Out = s }),
			contractfile.Field{Name: "opt", Read: func(n ast.Node) error {
				text, ok := contractfile.Scalar(n)
				if !ok {
					return fmt.Errorf("%w: %s.opt must be a scalar", ErrInvalid, where)
				}
				p.Opt = text
				return nil
			}},
			contractfile.Field{Name: "files", Read: func(n ast.Node) error {
				if _, isList := n.(*ast.SequenceNode); !isList {
					return fmt.Errorf("%w: %s.files must be a list of patterns", ErrInvalid, where)
				}
				err := contractfile.Sequence(n, where+".files", ErrInvalid, func(i int, item ast.Node) error {
					text, ok := contractfile.Line(item)
					if !ok {
						return fmt.Errorf("%w: %s.files[%d] must be one line of text", ErrInvalid, where, i)
					}
					if text == "" {
						return fmt.Errorf("%w: %s.files[%d] is empty", ErrInvalid, where, i)
					}
					if _, err := glob.Compile(text); err != nil {
						return fmt.Errorf("%w: %s.files[%d]: %v", ErrInvalid, where, i, err)
					}
					p.Files = append(p.Files, text)
					return nil
				})
				if err != nil {
					return err
				}
				if len(p.Files) == 0 {
					return fmt.Errorf("%w: %s.files names no pattern", ErrInvalid, where)
				}
				return nil
			}},
			boolField(where, "include_imports", &p.IncludeImports),
			boolField(where, "include_wkt", &p.IncludeWKT),
			boolField(where, "clean", &p.Clean),
		)
		if err != nil {
			return err
		}
		// A well-known import is reached through imports or not at all.
		if p.IncludeWKT && !p.IncludeImports {
			return fmt.Errorf("%w: %s.include_wkt without include_imports: a well-known import is reached through imports alone", ErrInvalid, where)
		}
		if schemes != 1 {
			return fmt.Errorf("%w: %s must carry exactly one of ref or local, found %d", ErrInvalid, where, schemes)
		}
		if !hasOut {
			return fmt.Errorf("%w: %s has no out", ErrInvalid, where)
		}
		if err := checkIdentity(p); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalid, where, err)
		}
		if err := CheckOut(p.Out); err != nil {
			return fmt.Errorf("%w: %s.out: %v", ErrInvalid, where, err)
		}
		plugins = append(plugins, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(plugins) == 0 {
		return nil, fmt.Errorf("%w: plugins must not be empty", ErrInvalid)
	}
	return plugins, nil
}

// boolField is a key under where read by flag into dst.
func boolField(where, name string, dst *bool) contractfile.Field {
	key := name
	if where != "" {
		key = where + "." + name
	}
	return contractfile.Field{Name: name, Read: func(n ast.Node) error {
		v, err := flag(n, key)
		*dst = v
		return err
	}}
}

// flag reads a key spelled `true` or `false`, and nothing else
// (REQ-gen-schema).
func flag(n ast.Node, where string) (bool, error) {
	text, ok := contractfile.Line(n)
	switch {
	case !ok:
		return false, fmt.Errorf("%w: %s must be true or false", ErrInvalid, where)
	case text == "true":
		return true, nil
	case text == "false":
		return false, nil
	}
	return false, fmt.Errorf("%w: %s must be true or false, not %q", ErrInvalid, where, text)
}

func checkIdentity(p Plugin) error {
	switch p.Scheme {
	case plugin.SchemeOCI:
		return CheckReference(p.Ref)
	case plugin.SchemeLocal:
		return CheckLocal(p.Ref)
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

// CheckLocal accepts a local plugin command (plugin-execution.md
// REQ-plugin-local-resolution): a bare program name, or a forward-slash
// path — relative or absolute. Backslashes are never separators.
func CheckLocal(s string) error {
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

// CheckOut validates an output directory (REQ-gen-schema): a clean
// relative forward-slash path that never escapes the resolution root
// — the written-spelling rule of committed configuration (backslashes
// are never separators) over the shared containment judgment.
func CheckOut(s string) error {
	if strings.ContainsRune(s, '\\') {
		return errors.New("paths are written with forward slashes")
	}
	return rootpath.Check(s, "the resolution root")
}

// parseOverrides reads the overrides list: each entry files and
// option, required, a value or a derivation — a prefix, a suffix,
// or neither where the option's rule stands alone — and at most one
// of module and except, a module path and a non-empty list of them
// — a glob, an option name and a module path one line of text, the
// value text as written.
func parseOverrides(n ast.Node) ([]Override, error) {
	overrides := []Override{}
	err := contractfile.Sequence(n, "overrides", ErrInvalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("overrides[%d]", i)
		var o Override
		line := func(name string, into *string) contractfile.Field {
			return contractfile.Field{Name: name, Required: true, Read: func(n ast.Node) error {
				text, ok := contractfile.Line(n)
				if !ok {
					return fmt.Errorf("%w: %s.%s must be one line of text", ErrInvalid, where, name)
				}
				*into = text
				return nil
			}}
		}
		hasValue, hasDerivation := false, false
		axis := func(name string, into *string) contractfile.Field {
			f := line(name, into)
			f.Required = false
			read := f.Read
			f.Read = func(n ast.Node) error {
				if err := read(n); err != nil {
					return err
				}
				if *into == "" {
					return fmt.Errorf("%w: %s.%s is empty", ErrInvalid, where, name)
				}
				hasDerivation = true
				return nil
			}
			return f
		}
		modulePath := func(text, key string) error {
			if err := module.ValidatePath(text); err != nil {
				return fmt.Errorf("%w: %s: %v", ErrInvalid, key, err)
			}
			return nil
		}
		moduleField := line("module", &o.Module)
		moduleField.Required = false
		readModule := moduleField.Read
		moduleField.Read = func(n ast.Node) error {
			if err := readModule(n); err != nil {
				return err
			}
			return modulePath(o.Module, where+".module")
		}
		err := contractfile.Mapping(en, where, ErrInvalid,
			line("files", &o.Files),
			moduleField,
			contractfile.Field{Name: "except", Read: func(n ast.Node) error {
				if _, isList := n.(*ast.SequenceNode); !isList {
					return fmt.Errorf("%w: %s.except must be a list of module paths", ErrInvalid, where)
				}
				err := contractfile.Sequence(n, where+".except", ErrInvalid, func(i int, item ast.Node) error {
					text, ok := contractfile.Line(item)
					if !ok {
						return fmt.Errorf("%w: %s.except[%d] must be one line of text", ErrInvalid, where, i)
					}
					if err := modulePath(text, fmt.Sprintf("%s.except[%d]", where, i)); err != nil {
						return err
					}
					if slices.Contains(o.Except, text) {
						return fmt.Errorf("%w: %s.except[%d] names %s twice", ErrInvalid, where, i, text)
					}
					o.Except = append(o.Except, text)
					return nil
				})
				if err != nil {
					return err
				}
				if len(o.Except) == 0 {
					return fmt.Errorf("%w: %s.except names no module", ErrInvalid, where)
				}
				return nil
			}},
			line("option", &o.Option),
			contractfile.Field{Name: "value", Read: func(n ast.Node) error {
				text, ok := contractfile.Scalar(n)
				if !ok {
					return fmt.Errorf("%w: %s.value must be a scalar", ErrInvalid, where)
				}
				o.Value, hasValue = text, true
				return nil
			}},
			axis("prefix", &o.Prefix),
			axis("suffix", &o.Suffix),
		)
		if err != nil {
			return err
		}
		switch {
		case hasValue && hasDerivation:
			return fmt.Errorf("%w: %s carries a value and a derivation; one or the other", ErrInvalid, where)
		case !hasValue && !hasDerivation:
			// Neither: a derivation from the file alone, where the
			// option's rule stands without an axis (CheckDerivation).
			o.Bare = true
		}
		if o.Module != "" && len(o.Except) > 0 {
			return fmt.Errorf("%w: %s names a module and excepts some; one or the other", ErrInvalid, where)
		}
		if _, err := glob.Compile(o.Files); err != nil {
			return fmt.Errorf("%w: %s.files: %v", ErrInvalid, where, err)
		}
		if err := checkOptionName(o.Option); err != nil {
			return fmt.Errorf("%w: %s.option: %v", ErrInvalid, where, err)
		}
		if o.Derived() {
			if err := CheckDerivation(o); err != nil {
				return fmt.Errorf("%w: %s: %v", ErrInvalid, where, err)
			}
		}
		overrides = append(overrides, o)
		return nil
	})
	if err != nil {
		return nil, err
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
