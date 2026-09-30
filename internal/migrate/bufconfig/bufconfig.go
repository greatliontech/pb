// Package bufconfig reads a buf configuration as buf's own files spell
// it (migrate.md, the buf configuration term): buf.yaml in its v1 and
// v2 forms, buf.work.yaml, buf.gen.yaml in its v1 and v2 forms, and
// buf.lock in its v1 and v2 forms, each under the version it declares.
// The read is deliberately lenient where a contract file's is strict:
// a key the package does not model is no fault of the file but an
// unmapped fact the migration reports, so every reader collects the
// keys it passed over, spelled by path (REQ-migrate-verb). What it
// reads is buf's meaning as buf documents it; nothing here is pb's
// configuration, which the verb derives.
package bufconfig

import (
	"errors"
	"fmt"
	"golang.org/x/mod/semver"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/rootpath"
)

// The names of buf's configuration files at a directory.
const (
	FileName     = "buf.yaml"
	WorkFileName = "buf.work.yaml"
	GenFileName  = "buf.gen.yaml"
	// genTemplatePrefix and genTemplateSuffix frame a generation
	// template's name, `buf.gen.<name>.yaml`, the one `buf generate
	// --template` names.
	genTemplatePrefix = "buf.gen."
	genTemplateSuffix = ".yaml"
	LockFileName      = "buf.lock"
)

// ErrInvalid is wrapped by every refusal: a file that is no YAML
// mapping, one whose version is missing, unknown or unsupported, or
// a modeled key whose value is not what buf accepts there.
var ErrInvalid = errors.New("invalid buf configuration")

// Unmodeled is a key the reader passed over, by path within its file
// ("lint.foo", "plugins[1].bar"): an unmapped fact of the migration.
type Unmodeled string

// Section is buf's lint or breaking section, the keys the two share:
// the categories or ids to use and to except, paths to ignore
// wholesale, and paths to ignore for named rules.
type Section struct {
	Use        []string
	Except     []string
	Ignore     []string
	IgnoreOnly map[string][]string
	// The rule-shaping options, each with buf's own default where the
	// file says nothing: set means the file spelled the key.
	Options map[string]string
}

// Module is one module a buf.yaml declares: its directory relative to
// the file, its BSR name, its own sections where a v2 file gives it
// any, and its file-set narrowing.
type Module struct {
	Path     string
	Name     string
	Lint     *Section
	Breaking *Section
	Excludes []string
	Includes []string
}

// File is a buf.yaml: its version, its modules (one, at ".", for v1),
// its dependencies as spelled, the file-level lint and breaking
// sections, and the keys passed over.
type File struct {
	Version   string
	Modules   []Module
	Deps      []string
	Lint      *Section
	Breaking  *Section
	Unmodeled []Unmodeled
}

// Work is a buf.work.yaml: the directories it names.
type Work struct {
	Version     string
	Directories []string
	Unmodeled   []Unmodeled
}

// Plugin is one entry of buf.gen.yaml's plugins in one of its forms as
// buf reads them — a BSR plugin reference (`remote`, or v1's `plugin`
// spelled `remote/owner/plugin` with a `:version` or none), a local
// command (`local`, one word or several; v1's `path`, or the short
// name buf execs as protoc-gen-<name>), a protoc builtin (v2's
// `protoc_builtin`; v1's name that is one, or any with `protoc_path`)
// — with its output directory, its options joined as buf hands them
// to the plugin, and the keys passed over per entry.
type Plugin struct {
	Remote        string
	Local         []string
	ProtocBuiltin string
	Out           string
	Opt           string
	// IncludeImports is v2's include_imports.
	IncludeImports bool
	Unmodeled      []Unmodeled
}

// Input is one v2 `inputs` entry: the kind key naming it (`directory`,
// `module`, `git_repo` and the rest buf takes) with its value, the
// paths it restricts to and excludes, and the keys passed over.
type Input struct {
	Kind, Value  string
	Paths        []string
	ExcludePaths []string
	Unmodeled    []Unmodeled
}

// Override is one managed-mode override as v2 spells it, or as v1's
// `override` map implies: the option, its value, and the module, path
// or field it scopes to, empty for every file.
type Override struct {
	FileOption  string
	FieldOption string
	Value       string
	Module      string
	Path        string
	Field       string
}

// Form is one of v1 managed mode's option forms — `go_package_prefix`,
// `optimize_for` and their kin — as buf reads it: the option key, its
// default where the key takes one, the modules excepted from it and
// the per-module values; a scalar `java_package_prefix` or
// `optimize_for` is the default alone.
type Form struct {
	Option   string
	Default  string
	Except   []string
	Override map[string]string
}

// Managed is buf.gen.yaml's managed mode: whether enabled, the
// declarative overrides (v2's, and v1's boolean options and override
// map), v2's disables spelled for the report, and v1's option forms.
type Managed struct {
	Enabled   bool
	Overrides []Override
	Disables  []string // v2 disable entries, spelled as written
	Forms     []Form   // v1 option forms, in document order
}

// Gen is a buf.gen.yaml: its version, its plugins, its managed mode
// where present, its inputs and clean, and the keys passed over.
type Gen struct {
	Version string
	Plugins []Plugin
	Managed *Managed
	// Inputs are v2's inputs in order, nil where the key is absent.
	Inputs []Input
	// Clean is v2's clean.
	Clean     bool
	Unmodeled []Unmodeled
}

// Dep is one buf.lock entry: the BSR module name, its commit and
// digest as buf recorded them.
type Dep struct {
	Name   string
	Commit string
	Digest string
}

// Lock is a buf.lock: its version and its entries.
type Lock struct {
	Version   string
	Deps      []Dep
	Unmodeled []Unmodeled
}

// ParseFile reads a buf.yaml.
func ParseFile(data []byte) (*File, error) {
	r, m, err := open(data, FileName, "v1", "v2")
	if err != nil {
		return nil, r.named(err)
	}
	f := &File{Version: r.version}
	fields := []contractfile.Field{
		{Name: "version"},
		{Name: "deps", Read: func(n ast.Node) error { return r.strings(n, "deps", &f.Deps) }},
		{Name: "lint", Read: func(n ast.Node) error { return r.section(n, "lint", &f.Lint, lintOptions[r.version]) }},
		{Name: "breaking", Read: func(n ast.Node) error { return r.section(n, "breaking", &f.Breaking, breakingOptions[r.version]) }},
	}
	switch r.version {
	case "v1":
		one := Module{Path: "."}
		fields = append(fields,
			contractfile.Field{Name: "name", Read: func(n ast.Node) error { return r.line(n, "name", &one.Name) }},
			contractfile.Field{Name: "build", Read: func(n ast.Node) error {
				return r.walk(n, "build", contractfile.Field{Name: "excludes", Read: func(n ast.Node) error { return r.strings(n, "build.excludes", &one.Excludes) }})
			}},
		)
		if err := r.walk(m, "", fields...); err != nil {
			return nil, r.named(err)
		}
		f.Modules = []Module{one}
	case "v2":
		fields = append(fields, contractfile.Field{Name: "modules", Read: func(n ast.Node) error {
			return contractfile.Sequence(n, "modules", ErrInvalid, func(i int, item ast.Node) error {
				where := fmt.Sprintf("modules[%d]", i)
				mod := Module{}
				err := r.walk(item, where,
					contractfile.Field{Name: "path", Required: true, Read: func(n ast.Node) error { return r.line(n, where+".path", &mod.Path) }},
					contractfile.Field{Name: "name", Read: func(n ast.Node) error { return r.line(n, where+".name", &mod.Name) }},
					contractfile.Field{Name: "excludes", Read: func(n ast.Node) error { return r.strings(n, where+".excludes", &mod.Excludes) }},
					contractfile.Field{Name: "includes", Read: func(n ast.Node) error { return r.strings(n, where+".includes", &mod.Includes) }},
					contractfile.Field{Name: "lint", Read: func(n ast.Node) error { return r.section(n, where+".lint", &mod.Lint, lintOptions[r.version]) }},
					contractfile.Field{Name: "breaking", Read: func(n ast.Node) error {
						return r.section(n, where+".breaking", &mod.Breaking, breakingOptions[r.version])
					}},
				)
				if err != nil {
					return err
				}
				// What buf refuses: a module name declared twice; the
				// directories are judged together below.
				for _, prior := range f.Modules {
					if mod.Name != "" && prior.Name == mod.Name {
						return fmt.Errorf("%w: %s: module name %q declared twice", ErrInvalid, where, mod.Name)
					}
				}
				f.Modules = append(f.Modules, mod)
				return nil
			})
		}})
		if err := r.walk(m, "", fields...); err != nil {
			return nil, r.named(err)
		}
		if len(f.Modules) == 0 {
			// A v2 file naming no module declares one at its directory.
			f.Modules = []Module{{Path: "."}}
		}
	}
	if r.version == "v2" {
		dirs := make([]string, len(f.Modules))
		for i, m := range f.Modules {
			dirs[i] = m.Path
		}
		if err := directories(dirs, "module directory", "seen more than once", "the configuration's directory", false); err != nil {
			return nil, r.named(err)
		}
	}
	// A dependency declared twice, its :ref aside, as buf keys them.
	seen := map[string]bool{}
	for _, d := range f.Deps {
		name, _, _ := strings.Cut(d, ":")
		if seen[name] {
			return nil, r.named(fmt.Errorf("%w: dependency %q declared twice", ErrInvalid, name))
		}
		seen[name] = true
	}
	f.Unmodeled = r.unmodeled
	return f, nil
}

// ParseWork reads a buf.work.yaml.
func ParseWork(data []byte) (*Work, error) {
	r, m, err := open(data, WorkFileName, "v1")
	if err != nil {
		return nil, r.named(err)
	}
	w := &Work{Version: r.version}
	err = r.walk(m, "",
		contractfile.Field{Name: "version"},
		contractfile.Field{Name: "directories", Required: true, Read: func(n ast.Node) error { return r.strings(n, "directories", &w.Directories) }},
	)
	if err != nil {
		return nil, r.named(err)
	}
	// What buf refuses of the directories: none; one listed twice; the
	// directory itself; one containing another.
	if len(w.Directories) == 0 {
		return nil, r.named(fmt.Errorf("%w: directories is empty", ErrInvalid))
	}
	if err := directories(w.Directories, "directory", "is listed more than once", "the workspace directory", true); err != nil {
		return nil, r.named(err)
	}
	w.Unmodeled = r.unmodeled
	return w, nil
}

// directories refuses what buf refuses of a list of directories under
// a root, in buf's words for the noun and the duplicate phrase given:
// an entry escaping the root, one listed twice (after cleaning) and,
// where nesting is refused, the root itself and an entry containing
// another. The entries stay as spelled: the reader keeps buf's file as
// buf wrote it, its consumers cleaning.
func directories(entries []string, noun, twice, root string, nesting bool) error {
	spelled := map[string]string{}
	for _, d := range entries {
		c, err := rootpath.Clean(d, root)
		if err != nil {
			return fmt.Errorf("%w: %s %q: %v", ErrInvalid, noun, d, err)
		}
		if _, dup := spelled[c]; dup {
			return fmt.Errorf("%w: %s %q %s", ErrInvalid, noun, d, twice)
		}
		if nesting {
			if c == "." {
				return fmt.Errorf("%w: %s %q is the workspace directory itself", ErrInvalid, noun, d)
			}
			for other, prior := range spelled {
				if rootpath.Contains(other, c) {
					return fmt.Errorf("%w: %s %q contains %s %q", ErrInvalid, noun, prior, noun, d)
				}
				if rootpath.Contains(c, other) {
					return fmt.Errorf("%w: %s %q contains %s %q", ErrInvalid, noun, d, noun, prior)
				}
			}
		}
		spelled[c] = d
	}
	return nil
}

// ParseGen reads a buf.gen.yaml.
func ParseGen(data []byte) (*Gen, error) {
	r, m, err := open(data, GenFileName, "v1", "v2")
	if err != nil {
		return nil, r.named(err)
	}
	g := &Gen{Version: r.version}
	plugins := contractfile.Field{Name: "plugins", Read: func(n ast.Node) error {
		return contractfile.Sequence(n, "plugins", ErrInvalid, func(i int, item ast.Node) error {
			where := fmt.Sprintf("plugins[%d]", i)
			p, err := r.plugin(item, where)
			if err != nil {
				return err
			}
			g.Plugins = append(g.Plugins, p)
			return nil
		})
	}}
	managed := contractfile.Field{Name: "managed", Read: func(n ast.Node) error {
		mg, err := r.managed(n, "managed")
		g.Managed = mg
		return err
	}}
	fields := []contractfile.Field{{Name: "version"}, plugins, managed}
	if r.version == "v2" {
		fields = append(fields,
			contractfile.Field{Name: "inputs", Read: func(n ast.Node) error {
				g.Inputs = []Input{}
				return contractfile.Sequence(n, "inputs", ErrInvalid, func(i int, item ast.Node) error {
					in, err := r.input(item, fmt.Sprintf("inputs[%d]", i))
					if err != nil {
						return err
					}
					g.Inputs = append(g.Inputs, in)
					return nil
				})
			}},
			contractfile.Field{Name: "clean", Read: func(n ast.Node) error { return r.flag(n, "clean", &g.Clean) }},
		)
	}
	if err := r.walk(m, "", fields...); err != nil {
		return nil, r.named(err)
	}
	g.Unmodeled = r.unmodeled
	return g, nil
}

// ParseLock reads a buf.lock.
func ParseLock(data []byte) (*Lock, error) {
	r, m, err := open(data, LockFileName, "v1", "v2")
	if err != nil {
		return nil, r.named(err)
	}
	l := &Lock{Version: r.version}
	err = r.walk(m, "",
		contractfile.Field{Name: "version"},
		contractfile.Field{Name: "deps", Read: func(n ast.Node) error {
			return contractfile.Sequence(n, "deps", ErrInvalid, func(i int, item ast.Node) error {
				where := fmt.Sprintf("deps[%d]", i)
				var d Dep
				var remote, owner, repository string
				fields := []contractfile.Field{
					{Name: "commit", Read: func(n ast.Node) error { return r.line(n, where+".commit", &d.Commit) }},
					{Name: "digest", Read: func(n ast.Node) error { return r.line(n, where+".digest", &d.Digest) }},
				}
				if r.version == "v2" {
					fields = append(fields, contractfile.Field{Name: "name", Required: true, Read: func(n ast.Node) error { return r.line(n, where+".name", &d.Name) }})
				} else {
					fields = append(fields,
						contractfile.Field{Name: "remote", Required: true, Read: func(n ast.Node) error { return r.line(n, where+".remote", &remote) }},
						contractfile.Field{Name: "owner", Required: true, Read: func(n ast.Node) error { return r.line(n, where+".owner", &owner) }},
						contractfile.Field{Name: "repository", Required: true, Read: func(n ast.Node) error { return r.line(n, where+".repository", &repository) }},
					)
				}
				if err := r.walk(item, where, fields...); err != nil {
					return err
				}
				if r.version == "v1" {
					d.Name = remote + "/" + owner + "/" + repository
				}
				l.Deps = append(l.Deps, d)
				return nil
			})
		}},
	)
	if err != nil {
		return nil, r.named(err)
	}
	l.Unmodeled = r.unmodeled
	return l, nil
}

// The rule-shaping options each section admits, buf's spelling per
// version; another version's key is passed over as unmodeled.
var (
	lintOptions = map[string][]string{
		"v1": {"enum_zero_value_suffix", "rpc_allow_same_request_response", "rpc_allow_google_protobuf_empty_requests", "rpc_allow_google_protobuf_empty_responses", "service_suffix", "allow_comment_ignores"},
		"v2": {"enum_zero_value_suffix", "rpc_allow_same_request_response", "rpc_allow_google_protobuf_empty_requests", "rpc_allow_google_protobuf_empty_responses", "service_suffix", "disallow_comment_ignores"},
	}
	breakingOptions = map[string][]string{
		"v1": {"ignore_unstable_packages"},
		"v2": {"ignore_unstable_packages"},
	}
)

// reader is one file's read: its version and the keys passed over.
type reader struct {
	file      string
	version   string
	unmodeled []Unmodeled
}

// open parses the document and reads its version, refusing a version
// the file's form does not come in.
func open(data []byte, file string, versions ...string) (*reader, *ast.MappingNode, error) {
	m, err := contractfile.Doc(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrInvalid, file, err)
	}
	if m == nil {
		return nil, nil, fmt.Errorf("%w: %s: empty document, no version", ErrInvalid, file)
	}
	r := &reader{file: file}
	for _, kv := range m.Values {
		if contractfile.Key(kv.Key) == "version" {
			v, ok := contractfile.Line(kv.Value)
			if !ok {
				return nil, nil, fmt.Errorf("%w: %s: version must be one line of text", ErrInvalid, file)
			}
			r.version = v
		}
	}
	if r.version == "" {
		return nil, nil, fmt.Errorf("%w: %s: no version; buf's %s files carry one", ErrInvalid, file, strings.Join(versions, " or "))
	}
	for _, v := range versions {
		if r.version == v {
			return r, m, nil
		}
	}
	return nil, nil, fmt.Errorf("%w: %s: version %q is none of %s", ErrInvalid, file, r.version, strings.Join(versions, ", "))
}

// walk reads a mapping with the reader's two policies: a key the
// reader does not model is passed over by path, and a key with no
// value is a key absent, as buf's decoder reads a null — modeled or
// not — unless the key is required, when its reader refuses the null
// as buf does. The caller's fields are left as they were.
func (r *reader) walk(n ast.Node, where string, fields ...contractfile.Field) error {
	wrapped := make([]contractfile.Field, len(fields))
	for i, f := range fields {
		if !f.Required && f.Read != nil {
			read := f.Read
			f.Read = func(n ast.Node) error {
				if isNull(n) {
					return nil
				}
				return read(n)
			}
		}
		wrapped[i] = f
	}
	return contractfile.Walk(n, where, ErrInvalid, func(key string, value ast.Node) {
		if isNull(value) {
			return
		}
		if where != "" {
			key = where + "." + key
		}
		r.unmodeled = append(r.unmodeled, Unmodeled(key))
	}, wrapped...)
}

func isNull(n ast.Node) bool {
	_, null := n.(*ast.NullNode)
	return null
}

func (r *reader) line(n ast.Node, where string, into *string) error {
	s, ok := contractfile.Line(n)
	if !ok {
		return fmt.Errorf("%w: %s must be one line of text", ErrInvalid, where)
	}
	*into = s
	return nil
}

// inputKinds are the keys naming a v2 input's kind, one per entry.
var inputKinds = []string{"directory", "module", "git_repo", "tarball", "zip_archive", "proto_file", "binary_image", "json_image", "txt_image", "yaml_image"}

// input reads one v2 inputs entry: its kind key and value, paths and
// exclude_paths as lists, every other key passed over under the
// entry; no kind key or two is buf's own refusal.
func (r *reader) input(n ast.Node, where string) (Input, error) {
	var in Input
	forms := 0
	fields := []contractfile.Field{
		{Name: "paths", Read: func(n ast.Node) error { return r.strings(n, where+".paths", &in.Paths) }},
		{Name: "exclude_paths", Read: func(n ast.Node) error { return r.strings(n, where+".exclude_paths", &in.ExcludePaths) }},
	}
	for _, k := range inputKinds {
		fields = append(fields, contractfile.Field{Name: k, Read: func(n ast.Node) error {
			forms++
			in.Kind = k
			return r.line(n, where+"."+k, &in.Value)
		}})
	}
	before := len(r.unmodeled)
	if err := r.walk(n, where, fields...); err != nil {
		return in, err
	}
	if forms != 1 {
		return in, fmt.Errorf("%w: %s names %d input kinds, one expected", ErrInvalid, where, forms)
	}
	if in.Value == "" {
		return in, fmt.Errorf("%w: %s.%s names no input", ErrInvalid, where, in.Kind)
	}
	in.Unmodeled = append([]Unmodeled(nil), r.unmodeled[before:]...)
	r.unmodeled = r.unmodeled[:before]
	return in, nil
}

// flag reads a boolean as buf reads one (boolean).
func (r *reader) flag(n ast.Node, where string, into *bool) error {
	v, err := boolean(n, where)
	if err != nil {
		return err
	}
	*into = v == "true"
	return nil
}

func (r *reader) strings(n ast.Node, where string, into *[]string) error {
	l, err := contractfile.Strings(n, where, ErrInvalid)
	if err != nil {
		return err
	}
	*into = l
	return nil
}

// named wraps a reader's refusal with the file's name once, at the
// reader's boundary, so every message says which of the four files.
func (r *reader) named(err error) error {
	if err == nil || r == nil {
		return err
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalid, r.file, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
}

// section reads a lint or breaking section: the shared keys and the
// options the section admits, each option's value kept as written.
func (r *reader) section(n ast.Node, where string, into **Section, options []string) error {
	s := &Section{}
	fields := []contractfile.Field{
		{Name: "use", Read: func(n ast.Node) error { return r.strings(n, where+".use", &s.Use) }},
		{Name: "except", Read: func(n ast.Node) error { return r.strings(n, where+".except", &s.Except) }},
		{Name: "ignore", Read: func(n ast.Node) error { return r.strings(n, where+".ignore", &s.Ignore) }},
		{Name: "ignore_only", Read: func(n ast.Node) error {
			m, ok := n.(*ast.MappingNode)
			if !ok {
				return fmt.Errorf("%w: %s.ignore_only must be a mapping from rule to paths", ErrInvalid, where)
			}
			s.IgnoreOnly = map[string][]string{}
			for _, kv := range m.Values {
				id := contractfile.Key(kv.Key)
				var paths []string
				if err := r.strings(kv.Value, where+".ignore_only."+id, &paths); err != nil {
					return err
				}
				s.IgnoreOnly[id] = paths
			}
			return nil
		}},
	}
	for _, o := range options {
		name := o
		fields = append(fields, contractfile.Field{Name: name, Read: func(n ast.Node) error {
			v, ok := contractfile.Scalar(n)
			if !ok {
				return fmt.Errorf("%w: %s.%s must be a scalar", ErrInvalid, where, name)
			}
			if s.Options == nil {
				s.Options = map[string]string{}
			}
			s.Options[name] = v
			return nil
		}})
	}
	if err := r.walk(n, where, fields...); err != nil {
		return err
	}
	*into = s
	return nil
}

// protocBuiltins are the plugins buf hands to protoc by name, buf's
// own list.
var protocBuiltins = map[string]bool{"cpp": true, "csharp": true, "java": true, "js": true, "objc": true, "php": true, "python": true, "pyi": true, "ruby": true, "kotlin": true, "rust": true}

// isRemoteReference reports whether s is a plugin reference or
// identity as buf's v2 reader parses one: the version, where a colon
// follows the last slash, the text after it, a valid semver as buf's
// versions are (v29.2 among them); the name before it, or the whole
// where no colon does, `remote/owner/plugin`, three non-empty parts,
// the remote a host with a port or none.
func isRemoteReference(s string) bool {
	name := s
	if colon := strings.LastIndexByte(s, ':'); colon > strings.LastIndexByte(s, '/') {
		name = s[:colon]
		if !semver.IsValid(s[colon+1:]) {
			return false
		}
	}
	parts := strings.Split(name, "/")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return !strings.Contains(parts[2], ":")
}

// isPluginReference reports whether s is spelled as buf's plugin
// reference or identity — `remote/owner/plugin`, three non-empty
// parts, a `:version` after or none — buf's own test of whether a v1
// `plugin` names a remote plugin.
func isPluginReference(s string) bool {
	name, version, cut := strings.Cut(s, ":")
	if cut && version == "" {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

// v1Booleans and v1Forms are v1 managed mode's option keys — each a
// file option v2 spells under fileOptions — the booleans, and the forms
// with whether each takes a scalar shorthand and a default key.
var (
	v1Booleans = []string{"cc_enable_arenas", "java_multiple_files", "java_string_check_utf8"}
	v1Forms    = []struct {
		key             string
		scalar, defined bool
	}{
		{"java_package_prefix", true, true}, {"optimize_for", true, true},
		{"go_package_prefix", false, true}, {"objc_class_prefix", false, true}, {"swift_prefix", false, true},
		{"csharp_namespace", false, false}, {"ruby_package", false, false},
	}
)

// fileOptions and fieldOptions are the option names buf's v2 managed
// mode knows, read in any case and kept in lower case as buf reads
// them; another is buf's own refusal.
var (
	fileOptions  = set("java_package", "java_package_prefix", "java_package_suffix", "java_outer_classname", "java_multiple_files", "java_string_check_utf8", "optimize_for", "go_package", "go_package_prefix", "cc_enable_arenas", "objc_class_prefix", "csharp_namespace", "csharp_namespace_prefix", "php_namespace", "php_metadata_namespace", "php_metadata_namespace_suffix", "ruby_package", "ruby_package_suffix", "swift_prefix")
	fieldOptions = set("jstype")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// optionName is the one reading of an option name against a set buf
// knows: lowered as buf reads every option name, refused where unknown.
func optionName(s, where string, known map[string]bool) (string, error) {
	s = strings.ToLower(s)
	if !known[s] {
		return "", fmt.Errorf("%w: %s %q is no option buf knows", ErrInvalid, where, s)
	}
	return s, nil
}

// option reads a v2 file_option or field_option value, trimmed as
// buf's v2 reader trims it before the name is read.
func (r *reader) option(n ast.Node, where string, known map[string]bool, into *string) error {
	if err := r.line(n, where, into); err != nil {
		return err
	}
	name, err := optionName(strings.TrimSpace(*into), where, known)
	*into = name
	return err
}

// takesNo lists, per version and form, the keys buf refuses beside
// that form; v1 refuses keys beside a remote plugin alone.
var takesNo = map[string][]string{
	"v1 remote":  {"path", "strategy", "protoc_path"},
	"v2 remote":  {"strategy", "protoc_path"},
	"v2 local":   {"revision", "protoc_path"},
	"v2 builtin": {"revision"},
}

// plugin reads one plugins entry in the forms its version spells and
// refuses what buf refuses: no naming form or two, an empty one; v1's
// `name` spelled as a plugin reference; a key no form of the entry's
// kind takes. v1's
// form follows buf's reading — `plugin` spelled as a reference is
// remote, `path` makes a local command, `protoc_path` a protoc
// builtin, a protoc builtin's name a builtin, and any other name the
// executable protoc-gen-<name>. The options join with commas as buf
// joins a list; `strategy`, `revision`, `protoc_path` and v2's
// `include_imports` and `include_wkt` are passed over for the report,
// their presence read for the refusals alone.
func (r *reader) plugin(n ast.Node, where string) (Plugin, error) {
	var p Plugin
	var opts, path []string
	before := len(r.unmodeled)
	forms := 0
	var short, key string // v1: the naming key used and its value
	present := map[string]bool{}
	naming := func(k string, into *string) contractfile.Field {
		return contractfile.Field{Name: k, Read: func(n ast.Node) error {
			if err := r.line(n, where+"."+k, into); err != nil {
				return err
			}
			if *into == "" {
				return fmt.Errorf("%w: %s.%s names no plugin", ErrInvalid, where, k)
			}
			forms++
			key = k
			return nil
		}}
	}
	noted := func(k string) contractfile.Field {
		return contractfile.Field{Name: k, Read: func(ast.Node) error {
			present[k] = true
			r.unmodeled = append(r.unmodeled, Unmodeled(where+"."+k))
			return nil
		}}
	}
	command := func(k string, into *[]string) contractfile.Field {
		return contractfile.Field{Name: k, Read: func(n ast.Node) error {
			present[k] = true
			if s, ok := contractfile.Line(n); ok {
				*into = []string{s}
			} else if err := r.strings(n, where+"."+k, into); err != nil {
				return err
			}
			if len(*into) == 0 || (*into)[0] == "" {
				return fmt.Errorf("%w: %s.%s names no command", ErrInvalid, where, k)
			}
			return nil
		}}
	}
	fields := []contractfile.Field{
		{Name: "out", Read: func(n ast.Node) error { return r.line(n, where+".out", &p.Out) }},
		{Name: "opt", Read: func(n ast.Node) error {
			if s, ok := contractfile.Line(n); ok {
				opts = []string{s}
				return nil
			}
			// An entry may be empty: buf joins the list as it is.
			return contractfile.Sequence(n, where+".opt", ErrInvalid, func(i int, item ast.Node) error {
				s, ok := contractfile.Line(item)
				if !ok {
					return fmt.Errorf("%w: %s.opt[%d] must be one line of text", ErrInvalid, where, i)
				}
				opts = append(opts, s)
				return nil
			})
		}},
		noted("strategy"), noted("revision"), noted("protoc_path"),
	}
	switch r.version {
	case "v2":
		fields = append(fields,
			naming("remote", &p.Remote),
			naming("protoc_builtin", &p.ProtocBuiltin),
			contractfile.Field{Name: "local", Read: func(n ast.Node) error {
				forms++
				return command("local", &p.Local).Read(n)
			}},
			contractfile.Field{Name: "include_imports", Read: func(n ast.Node) error { return r.flag(n, where+".include_imports", &p.IncludeImports) }},
			noted("include_wkt"),
		)
	case "v1":
		fields = append(fields, naming("plugin", &short), naming("name", &short), command("path", &path))
	}
	if err := r.walk(n, where, fields...); err != nil {
		return p, err
	}
	if forms == 0 && r.version == "v1" && slices.Contains(r.unmodeled[before:], Unmodeled(where+".remote")) {
		// v1's alpha remote plugin, run by the BSR: a form pb does
		// not model, the entry read for its out and opt and the key
		// passed over for the report; the keys buf refuses beside a
		// remote plugin refused here too.
		for _, k := range takesNo["v1 remote"] {
			if present[k] {
				return p, fmt.Errorf("%w: %s: a remote plugin takes no %s", ErrInvalid, where, k)
			}
		}
		if p.Out == "" {
			return p, fmt.Errorf("%w: %s has no out", ErrInvalid, where)
		}
		p.Opt = strings.Join(opts, ",")
		p.Unmodeled = append([]Unmodeled(nil), r.unmodeled[before:]...)
		r.unmodeled = r.unmodeled[:before]
		return p, nil
	}
	if forms != 1 {
		return p, fmt.Errorf("%w: %s names %d plugin forms, one expected", ErrInvalid, where, forms)
	}
	var form string
	switch {
	case r.version == "v2" && p.Remote != "":
		// A v2 remote is a plugin reference or identity, as buf parses
		// it: the version after the last colon a semver, the name
		// before it three parts.
		if !isRemoteReference(p.Remote) {
			return p, fmt.Errorf("%w: %s.remote %q is no plugin reference", ErrInvalid, where, p.Remote)
		}
		form = "remote"
	case r.version == "v2" && p.Local != nil:
		form = "local"
	case r.version == "v2":
		form = "builtin"
	case isPluginReference(short):
		if key == "name" {
			return p, fmt.Errorf("%w: %s: name %q is a plugin reference, no local plugin name", ErrInvalid, where, short)
		}
		p.Remote, form = short, "remote"
	case present["path"]:
		p.Local, form = path, "local"
	case present["protoc_path"] || protocBuiltins[short]:
		p.ProtocBuiltin, form = short, "builtin"
	default:
		p.Local, form = []string{"protoc-gen-" + short}, "local"
	}
	for _, k := range takesNo[r.version+" "+form] {
		if present[k] {
			return p, fmt.Errorf("%w: %s: a %s plugin takes no %s", ErrInvalid, where, form, k)
		}
	}
	if p.Out == "" {
		return p, fmt.Errorf("%w: %s has no out", ErrInvalid, where)
	}
	p.Opt = strings.Join(opts, ",")
	p.Unmodeled = append([]Unmodeled(nil), r.unmodeled[before:]...)
	r.unmodeled = r.unmodeled[:before]
	return p, nil
}

// managed reads managed mode in either version, refusing what buf
// refuses: v2's enabled, its override entries (one option, a value)
// and disable entries (not empty); v1's enabled, its boolean options
// as overrides for every file, its option forms, and its override map
// by option and file.
func (r *reader) managed(n ast.Node, where string) (*Managed, error) {
	mg := &Managed{}
	enabled := contractfile.Field{Name: "enabled", Read: func(n ast.Node) error {
		v, err := boolean(n, where+".enabled")
		mg.Enabled = v == "true"
		return err
	}}
	if r.version == "v2" {
		err := r.walk(n, where, enabled,
			contractfile.Field{Name: "override", Read: func(n ast.Node) error {
				return contractfile.Sequence(n, where+".override", ErrInvalid, func(i int, item ast.Node) error {
					w := fmt.Sprintf("%s.override[%d]", where, i)
					var o Override
					valued := false
					err := r.walk(item, w,
						contractfile.Field{Name: "file_option", Read: func(n ast.Node) error { return r.option(n, w+".file_option", fileOptions, &o.FileOption) }},
						contractfile.Field{Name: "field_option", Read: func(n ast.Node) error { return r.option(n, w+".field_option", fieldOptions, &o.FieldOption) }},
						contractfile.Field{Name: "value", Read: func(n ast.Node) error {
							v, ok := contractfile.Scalar(n)
							if !ok {
								return fmt.Errorf("%w: %s.value must be a scalar", ErrInvalid, w)
							}
							o.Value, valued = v, true
							return nil
						}},
						contractfile.Field{Name: "module", Read: func(n ast.Node) error { return r.line(n, w+".module", &o.Module) }},
						contractfile.Field{Name: "path", Read: func(n ast.Node) error { return r.line(n, w+".path", &o.Path) }},
						contractfile.Field{Name: "field", Read: func(n ast.Node) error { return r.line(n, w+".field", &o.Field) }},
					)
					if err != nil {
						return err
					}
					// What buf refuses of an override.
					switch {
					case o.FileOption == "" && o.FieldOption == "":
						return fmt.Errorf("%w: %s names no file_option or field_option", ErrInvalid, w)
					case o.FileOption != "" && o.FieldOption != "":
						return fmt.Errorf("%w: %s names both file_option and field_option", ErrInvalid, w)
					case !valued:
						return fmt.Errorf("%w: %s has no value", ErrInvalid, w)
					case o.Field != "" && o.FileOption != "":
						return fmt.Errorf("%w: %s scopes a file_option to a field", ErrInvalid, w)
					}
					mg.Overrides = append(mg.Overrides, o)
					return nil
				})
			}},
			contractfile.Field{Name: "disable", Read: func(n ast.Node) error {
				return contractfile.Sequence(n, where+".disable", ErrInvalid, func(i int, item ast.Node) error {
					w := fmt.Sprintf("%s.disable[%d]", where, i)
					var parts []string
					has := map[string]bool{}
					named := func(k string, known map[string]bool) contractfile.Field {
						return contractfile.Field{Name: k, Read: func(n ast.Node) error {
							has[k] = true
							if known != nil {
								var name string
								if err := r.option(n, w+"."+k, known, &name); err != nil {
									return err
								}
								parts = append(parts, k+"="+name)
								return nil
							}
							return keyed(n, k, &parts)
						}}
					}
					err := r.walk(item, w, named("file_option", fileOptions), named("field_option", fieldOptions), named("module", nil), named("path", nil), named("field", nil))
					if err != nil {
						return err
					}
					// What buf refuses of a disable rule.
					switch {
					case len(parts) == 0:
						return fmt.Errorf("%w: %s is empty", ErrInvalid, w)
					case has["file_option"] && has["field_option"]:
						return fmt.Errorf("%w: %s names both file_option and field_option", ErrInvalid, w)
					case has["file_option"] && has["field"]:
						return fmt.Errorf("%w: %s scopes a file_option to a field", ErrInvalid, w)
					}
					mg.Disables = append(mg.Disables, w+" "+strings.Join(parts, " "))
					return nil
				})
			}},
		)
		return mg, err
	}
	// v1: a boolean option a value for every file; a per-package form
	// a mapping of default, except and override, `java_package_prefix`
	// and `optimize_for` taking a scalar as their default alone;
	// `override` a map from option to a map from file to value. The
	// overrides hold buf's rule order, whatever the document's: the
	// booleans, then the per-file map by option key as written and
	// then by file path, in byte order.
	fields := []contractfile.Field{enabled}
	type perFile struct {
		key string // the option key as written
		o   Override
	}
	var perFiles []perFile
	for _, opt := range v1Booleans {
		option := opt
		fields = append(fields, contractfile.Field{Name: option, Read: func(n ast.Node) error {
			v, err := boolean(n, where+"."+option)
			if err != nil {
				return err
			}
			mg.Overrides = append(mg.Overrides, Override{FileOption: option, Value: v})
			return nil
		}})
	}
	for _, form := range v1Forms {
		form := form
		fields = append(fields, contractfile.Field{Name: form.key, Read: func(n ast.Node) error {
			w := where + "." + form.key
			f := Form{Option: form.key}
			if s, ok := contractfile.Line(n); ok && form.scalar {
				f.Default = s
				mg.Forms = append(mg.Forms, f)
				return nil
			}
			if _, ok := n.(*ast.MappingNode); !ok {
				return fmt.Errorf("%w: %s must be a mapping of default, except and override", ErrInvalid, w)
			}
			keys := []contractfile.Field{
				{Name: "except", Read: func(n ast.Node) error { return r.strings(n, w+".except", &f.Except) }},
				{Name: "override", Read: func(n ast.Node) error {
					om, ok := n.(*ast.MappingNode)
					if !ok {
						return fmt.Errorf("%w: %s.override must be a mapping from module to value", ErrInvalid, w)
					}
					f.Override = map[string]string{}
					for _, kv := range om.Values {
						v, ok := contractfile.Scalar(kv.Value)
						if !ok {
							return fmt.Errorf("%w: %s.override values must be scalars", ErrInvalid, w)
						}
						f.Override[contractfile.Key(kv.Key)] = v
					}
					return nil
				}},
			}
			if form.defined {
				keys = append(keys, contractfile.Field{Name: "default", Read: func(n ast.Node) error { return r.line(n, w+".default", &f.Default) }})
			}
			if err := r.walk(n, w, keys...); err != nil {
				return err
			}
			mg.Forms = append(mg.Forms, f)
			return nil
		}})
	}
	fields = append(fields, contractfile.Field{Name: "override", Read: func(n ast.Node) error {
		om, ok := n.(*ast.MappingNode)
		if !ok {
			return fmt.Errorf("%w: %s.override must be a mapping from option to files", ErrInvalid, where)
		}
		for _, okv := range om.Values {
			option, err := optionName(contractfile.Key(okv.Key), where+".override", fileOptions)
			if err != nil {
				return err
			}
			fm, ok := okv.Value.(*ast.MappingNode)
			if !ok {
				return fmt.Errorf("%w: %s.override.%s must be a mapping from file to value", ErrInvalid, where, option)
			}
			for _, fkv := range fm.Values {
				v, ok := contractfile.Scalar(fkv.Value)
				if !ok {
					return fmt.Errorf("%w: %s.override.%s values must be scalars", ErrInvalid, where, option)
				}
				perFiles = append(perFiles, perFile{contractfile.Key(okv.Key), Override{FileOption: option, Value: v, Path: contractfile.Key(fkv.Key)}})
			}
		}
		return nil
	}})
	if err := r.walk(n, where, fields...); err != nil {
		return nil, err
	}
	sort.SliceStable(perFiles, func(i, j int) bool {
		if perFiles[i].key != perFiles[j].key {
			return perFiles[i].key < perFiles[j].key
		}
		return perFiles[i].o.Path < perFiles[j].o.Path
	})
	for _, pf := range perFiles {
		mg.Overrides = append(mg.Overrides, pf.o)
	}
	return mg, nil
}

// boolean reads a boolean as buf's YAML reader spells one — true or
// false in lower, title or upper case, unquoted — and returns it in
// lower case.
func boolean(n ast.Node, where string) (string, error) {
	v, ok := contractfile.Scalar(n)
	if tok := n.GetToken(); !ok || tok.Type == token.SingleQuoteType || tok.Type == token.DoubleQuoteType {
		return "", fmt.Errorf("%w: %s must be true or false", ErrInvalid, where)
	}
	switch v {
	case "true", "True", "TRUE":
		return "true", nil
	case "false", "False", "FALSE":
		return "false", nil
	}
	return "", fmt.Errorf("%w: %s must be true or false", ErrInvalid, where)
}

// keyed records a key's scalar value as "key=value" for the report.
func keyed(n ast.Node, key string, into *[]string) error {
	v, ok := contractfile.Scalar(n)
	if !ok {
		return fmt.Errorf("%w: %s must be a scalar", ErrInvalid, key)
	}
	*into = append(*into, key+"="+v)
	return nil
}

// IsGenTemplate reports whether a file name is a generation template
// beside the configuration: `buf.gen.<name>.yaml` with a non-empty
// name — the generation file itself, one character short of the
// frame, is none.
func IsGenTemplate(name string) bool {
	return strings.HasPrefix(name, genTemplatePrefix) && strings.HasSuffix(name, genTemplateSuffix) &&
		len(name) > len(genTemplatePrefix)+len(genTemplateSuffix)
}
