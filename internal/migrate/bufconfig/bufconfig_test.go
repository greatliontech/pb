package bufconfig

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func join(u []Unmodeled) string {
	var out []string
	for _, k := range u {
		out = append(out, string(k))
	}
	return strings.Join(out, ",")
}

// A buf.yaml reads under its version: v1 one module at the file's
// directory with its name, deps, build excludes and sections; v2 its
// modules with their own sections, or one at the directory when none
// is named; every key the reader does not model is passed over by
// path, never refused (the buf configuration term, REQ-migrate-verb).
// Two unnamed v2 modules are the ordinary configuration, no name to
// collide on.
func TestParseFileUnnamedModules(t *testing.T) {
	f, err := ParseFile([]byte("version: v2\nmodules:\n  - path: a\n  - path: b\n"))
	if err != nil || len(f.Modules) != 2 {
		t.Fatalf("two unnamed modules: %+v %v", f, err)
	}
}

func TestParseFile(t *testing.T) {
	v1 := `version: v1
name: buf.build/acme/petapis
deps:
  - buf.build/googleapis/googleapis
  - buf.build/acme/paymentapis:v1
build:
  excludes: [vendor]
  roots: [proto]
lint:
  use: [DEFAULT, COMMENTS]
  except: [FIELD_LOWER_SNAKE_CASE]
  ignore: [legacy]
  ignore_only:
    ENUM_ZERO_VALUE_SUFFIX: [legacy/x.proto, old]
  enum_zero_value_suffix: _NONE
  rpc_allow_same_request_response: true
  allow_comment_ignores: false
  disallow_comment_ignores: true
  future: 1
breaking:
  use: [FILE]
  ignore_unstable_packages: true
extra: {}
`
	f, err := ParseFile([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != "v1" || len(f.Modules) != 1 || f.Modules[0].Path != "." || f.Modules[0].Name != "buf.build/acme/petapis" || strings.Join(f.Modules[0].Excludes, ",") != "vendor" || f.Modules[0].Lint != nil {
		t.Fatalf("v1 module: %+v", f.Modules)
	}
	if strings.Join(f.Deps, ",") != "buf.build/googleapis/googleapis,buf.build/acme/paymentapis:v1" {
		t.Fatalf("deps: %v", f.Deps)
	}
	l := f.Lint
	if l == nil || strings.Join(l.Use, ",") != "DEFAULT,COMMENTS" || strings.Join(l.Except, ",") != "FIELD_LOWER_SNAKE_CASE" || strings.Join(l.Ignore, ",") != "legacy" || strings.Join(l.IgnoreOnly["ENUM_ZERO_VALUE_SUFFIX"], ",") != "legacy/x.proto,old" || l.Options["enum_zero_value_suffix"] != "_NONE" || l.Options["rpc_allow_same_request_response"] != "true" || l.Options["allow_comment_ignores"] != "false" || len(l.Options) != 3 {
		t.Fatalf("lint: %+v", l)
	}
	if b := f.Breaking; b == nil || strings.Join(b.Use, ",") != "FILE" || b.Options["ignore_unstable_packages"] != "true" {
		t.Fatalf("breaking: %+v", f.Breaking)
	}
	if join(f.Unmodeled) != "build.roots,lint.disallow_comment_ignores,lint.future,extra" {
		t.Fatalf("unmodeled: %v", f.Unmodeled)
	}
	v2 := `version: v2
modules:
  - path: proto
    name: buf.build/acme/petapis
    lint:
      use: [STANDARD]
    excludes: [proto/gen]
  - path: legacy
    includes: [legacy/api]
    breaking:
      except: [FIELD_SAME_TYPE]
    custom: true
deps:
  - buf.build/googleapis/googleapis
lint:
  use: [BASIC]
  disallow_comment_ignores: true
plugins:
  - plugin: buf.build/bufbuild/protovalidate
`
	f, err = ParseFile([]byte(v2))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Modules) != 2 || f.Modules[0].Path != "proto" || f.Modules[0].Lint == nil || strings.Join(f.Modules[0].Lint.Use, ",") != "STANDARD" || f.Modules[0].Breaking != nil || strings.Join(f.Modules[0].Excludes, ",") != "proto/gen" || f.Modules[1].Path != "legacy" || strings.Join(f.Modules[1].Includes, ",") != "legacy/api" || f.Modules[1].Breaking == nil || strings.Join(f.Modules[1].Breaking.Except, ",") != "FIELD_SAME_TYPE" {
		t.Fatalf("v2 modules: %+v", f.Modules)
	}
	if f.Lint == nil || strings.Join(f.Lint.Use, ",") != "BASIC" || f.Lint.Options["disallow_comment_ignores"] != "true" || join(f.Unmodeled) != "modules[1].custom,plugins" {
		t.Fatalf("v2 file: %+v %v", f.Lint, f.Unmodeled)
	}
	f, err = ParseFile([]byte("version: v2\n"))
	if err != nil || len(f.Modules) != 1 || f.Modules[0].Path != "." {
		t.Fatalf("v2 with no modules: %+v %v", f, err)
	}
	for name, c := range map[string]struct{ in, want string }{
		"no version":       {"name: x\n", "buf.yaml: no version"},
		"named file":       {"version: v1\ndeps: x\n", "buf.yaml: deps must be a list"},
		"dir twice":        {"version: v2\nmodules:\n  - path: a\n  - path: ./a\n", `module directory "./a" seen more than once`},
		"name twice":       {"version: v2\nmodules:\n  - path: a\n    name: buf.build/x/y\n  - path: b\n    name: buf.build/x/y\n", `module name "buf.build/x/y" declared twice`},
		"dep twice":        {"version: v1\ndeps: [buf.build/x/y:aaa, buf.build/x/y:bbb]\n", `dependency "buf.build/x/y" declared twice`},
		"named nested":     {"version: v2\nmodules:\n  - name: x\n", "buf.yaml: modules[0]: missing path"},
		"v1beta1":          {"version: v1beta1\n", `version "v1beta1" is none of v1, v2`},
		"empty":            {"", "empty document"},
		"not yaml mapping": {"- a\n", "must be a mapping"},
		"deps not list":    {"version: v1\ndeps: x\n", "deps must be a list"},
		"module no path":   {"version: v2\nmodules:\n  - name: x\n", "missing path"},
		"ignore_only form": {"version: v1\nlint:\n  ignore_only: [x]\n", "ignore_only must be a mapping"},
	} {
		if _, err := ParseFile([]byte(c.in)); err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A buf.work.yaml names its directories; a buf.lock reads its
// entries in either version, a v1 entry's name joined from its parts.
func TestParseWorkAndLock(t *testing.T) {
	w, err := ParseWork([]byte("version: v1\ndirectories:\n  - proto\n  - vendor/x\nfuture: 1\n"))
	if err != nil || strings.Join(w.Directories, ",") != "proto,vendor/x" || join(w.Unmodeled) != "future" {
		t.Fatalf("work: %+v %v", w, err)
	}
	if _, err := ParseWork([]byte("version: v2\ndirectories: []\n")); err == nil || !strings.Contains(err.Error(), `version "v2" is none of v1`) {
		t.Fatalf("work v2: %v", err)
	}
	for name, c := range map[string]struct{ in, want string }{
		"empty":     {"version: v1\ndirectories: []\n", "directories is empty"},
		"twice":     {"version: v1\ndirectories: [a, ./a]\n", `directory "./a" is listed more than once`},
		"itself":    {"version: v1\ndirectories: [a, .]\n", `directory "." is the workspace directory itself`},
		"contains":  {"version: v1\ndirectories: [a/b, a]\n", `directory "a" contains directory "a/b"`},
		"contained": {"version: v1\ndirectories: [a, a/b]\n", `directory "a" contains directory "a/b"`},
		"escapes":   {"version: v1\ndirectories: [../a]\n", `directory "../a"`},
	} {
		if _, err := ParseWork([]byte(c.in)); err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("work %s: %v", name, err)
		}
	}
	if _, err := ParseWork([]byte("version: v1\n")); err == nil || !strings.Contains(err.Error(), "missing directories") {
		t.Fatalf("work without directories: %v", err)
	}
	l, err := ParseLock([]byte("version: v1\ndeps:\n  - remote: buf.build\n    owner: googleapis\n    repository: googleapis\n    commit: abc\n    digest: shake256:def\n    branch: main\n"))
	if err != nil || len(l.Deps) != 1 || l.Deps[0] != (Dep{Name: "buf.build/googleapis/googleapis", Commit: "abc", Digest: "shake256:def"}) || join(l.Unmodeled) != "deps[0].branch" {
		t.Fatalf("lock v1: %+v %v", l, err)
	}
	l, err = ParseLock([]byte("version: v2\ndeps:\n  - name: buf.build/googleapis/googleapis\n    commit: abc\n    digest: b5:def\n"))
	if err != nil || len(l.Deps) != 1 || l.Deps[0].Name != "buf.build/googleapis/googleapis" {
		t.Fatalf("lock v2: %+v %v", l, err)
	}
	if _, err := ParseLock([]byte("version: v2\ndeps:\n  - commit: abc\n")); err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Fatalf("lock v2 without name: %v", err)
	}
}

// A buf.gen.yaml's plugins read in both versions' naming forms with
// their options joined as buf joins them; managed mode reads v2's
// declarative overrides and disables and v1's option keys, prefix
// forms and override map; inputs are noted, not read
// (REQ-migrate-gen's input).
// A key with no value is a key absent, as buf's decoder reads a null,
// modeled or not; a required key with none is refused as buf refuses
// the empty value.
func TestNullIsAbsent(t *testing.T) {
	f, err := ParseFile([]byte("version: v1\ndeps:\nlint:\nbreaking:\nbuild:\nzzz:\n"))
	if err != nil || f.Deps != nil || f.Lint != nil || f.Breaking != nil || len(f.Unmodeled) != 0 {
		t.Fatalf("buf.yaml nulls: %+v %v", f, err)
	}
	if _, err := ParseFile([]byte("version: v2\nmodules:\n  - path:\n")); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("a required key with no value: %v", err)
	}
	g, err := ParseGen([]byte("version: v1\nplugins:\nmanaged:\n  ruby_package:\n  override:\ninputs:\n"))
	if err != nil || g.Plugins != nil || g.Managed == nil || g.Managed.Forms != nil || g.Managed.Overrides != nil || g.Inputs || len(g.Unmodeled) != 0 {
		t.Fatalf("buf.gen.yaml nulls: %+v %v", g, err)
	}
	g, err = ParseGen([]byte("version: v2\nplugins:\n  - local: x\n    out: o\n    opt:\nmanaged:\n  override:\n  disable:\n"))
	if err != nil || g.Plugins[0].Opt != "" || len(g.Managed.Overrides) != 0 || len(g.Managed.Disables) != 0 {
		t.Fatalf("v2 nulls: %+v %v", g, err)
	}
	if _, err := ParseWork([]byte("version: v1\ndirectories:\n")); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("buf.work.yaml's required directories with no value: %v", err)
	}
	l, err := ParseLock([]byte("version: v2\ndeps:\n"))
	if err != nil || l.Deps != nil {
		t.Fatalf("buf.lock null: %+v %v", l, err)
	}
}

// Every v1 managed option key is a file option v2 spells: one set of
// names, the v1 tables its subsets.
func TestV1ManagedKeysAreFileOptions(t *testing.T) {
	for _, k := range v1Booleans {
		if !fileOptions[k] {
			t.Errorf("v1 boolean %q is no v2 file option", k)
		}
	}
	for _, f := range v1Forms {
		if !fileOptions[f.key] {
			t.Errorf("v1 form %q is no v2 file option", f.key)
		}
	}
}

func TestParseGen(t *testing.T) {
	v2 := `version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/acme/gen
    - file_option: java_package
      value: com.acme
      path: proto/acme
    - field_option: JSTYPE
      value: JS_STRING
      module: buf.build/acme/petapis
    - field_option: jstype
      field: acme.v1.M.f
      value: JS_NORMAL
    - file_option: " Java_String_Check_Utf8 "
      value: "false"
  disable:
    - file_option: go_package
      module: buf.build/googleapis/googleapis
    - field: acme.v1.M.g
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.0
    out: gen/go
    opt: paths=source_relative
    include_imports: true
  - local: protoc-gen-connect-go
    out: gen/go
    opt: [paths=source_relative, package_suffix=]
  - local: [go, run, ./cmd/gen]
    out: gen/x
  - protoc_builtin: cpp
    out: gen/cpp
inputs:
  - directory: proto
clean: true
`
	g, err := ParseGen([]byte(v2))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Plugins) != 4 {
		t.Fatalf("plugins: %+v", g.Plugins)
	}
	p := g.Plugins
	if p[0].Remote != "buf.build/protocolbuffers/go:v1.36.0" || p[0].Out != "gen/go" || p[0].Opt != "paths=source_relative" || join(p[0].Unmodeled) != "plugins[0].include_imports" {
		t.Fatalf("remote: %+v", p[0])
	}
	if strings.Join(p[1].Local, " ") != "protoc-gen-connect-go" || p[1].Opt != "paths=source_relative,package_suffix=" || strings.Join(p[2].Local, " ") != "go run ./cmd/gen" || p[3].ProtocBuiltin != "cpp" || p[3].Out != "gen/cpp" {
		t.Fatalf("local and builtin: %+v %+v %+v", p[1], p[2], p[3])
	}
	m := g.Managed
	if m == nil || !m.Enabled || len(m.Overrides) != 5 || m.Overrides[0] != (Override{FileOption: "go_package_prefix", Value: "github.com/acme/gen"}) || m.Overrides[1] != (Override{FileOption: "java_package", Value: "com.acme", Path: "proto/acme"}) || m.Overrides[2] != (Override{FieldOption: "jstype", Value: "JS_STRING", Module: "buf.build/acme/petapis"}) || m.Overrides[3] != (Override{FieldOption: "jstype", Value: "JS_NORMAL", Field: "acme.v1.M.f"}) || m.Overrides[4] != (Override{FileOption: "java_string_check_utf8", Value: "false"}) {
		t.Fatalf("managed: %+v", m)
	}
	if len(m.Disables) != 2 || m.Disables[0] != "managed.disable[0] file_option=go_package module=buf.build/googleapis/googleapis" || m.Disables[1] != "managed.disable[1] field=acme.v1.M.g" || !g.Inputs || join(g.Unmodeled) != "clean" {
		t.Fatalf("disables, inputs, unmodeled: %+v %v %v", m.Disables, g.Inputs, g.Unmodeled)
	}
	v1 := `version: v1
managed:
  enabled: True
  java_multiple_files: TRUE
  optimize_for:
    default: SPEED
    except: [buf.build/acme/slow]
  java_package_prefix: com.acme
  csharp_namespace:
  go_package_prefix:
    default: github.com/acme/gen
    except: [buf.build/googleapis/googleapis]
    override:
      buf.build/acme/x: github.com/acme/x
  objc_class_prefix:
    default: ACM
  ruby_package:
    default: Acme
  override:
    JAVA_PACKAGE:
      acme/v1/a.proto: com.acme.a
plugins:
  - plugin: buf.build/protocolbuffers/go:v1.36.0
    out: gen/go
    opt: paths=source_relative
    revision: 1
  - name: go-grpc
    out: gen/go
    strategy: all
  - plugin: doc
    out: doc
    path: /usr/local/bin/protoc-gen-doc
  - plugin: java
    out: gen/java
  - plugin: example.com/org/foo
    out: gen/foo
  - plugin: buf.build/go
    out: gen/x
  - name: custom
    out: gen/c
    protoc_path: /opt/protoc
  - name: rust
    out: gen/rs
    path: [go, run, ./gen]
  - remote: buf.build/acme/plugins/legacy:v1
    plugin: x
    out: gen/x
  - plugin: "buf.build/acme/bare:"
    out: gen/b
  - plugin: rust
    out: gen/rs2
  - plugin: go
    out: gen/go
    revision: 1
    opt: [a, "", b]
  - plugin: buf.build/library/plugins/go
    out: gen/four
`
	g, err = ParseGen([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Plugins) != 13 {
		t.Fatalf("v1 plugins: %+v", g.Plugins)
	}
	for i, c := range []struct{ remote, local, builtin, unmodeled string }{
		{remote: "buf.build/protocolbuffers/go:v1.36.0", unmodeled: "plugins[0].revision"},
		{local: "protoc-gen-go-grpc", unmodeled: "plugins[1].strategy"},
		{local: "/usr/local/bin/protoc-gen-doc"},
		{builtin: "java"},
		{remote: "example.com/org/foo"},
		{local: "protoc-gen-buf.build/go"},
		{builtin: "custom", unmodeled: "plugins[6].protoc_path"},
		{local: "go run ./gen"},
		{local: "protoc-gen-x", unmodeled: "plugins[8].remote"},
		{local: "protoc-gen-buf.build/acme/bare:"},
		{builtin: "rust"},
		{local: "protoc-gen-go", unmodeled: "plugins[11].revision"},
		{local: "protoc-gen-buf.build/library/plugins/go"},
	} {
		p := g.Plugins[i]
		if p.Remote != c.remote || strings.Join(p.Local, " ") != c.local || p.ProtocBuiltin != c.builtin || join(p.Unmodeled) != c.unmodeled {
			t.Errorf("v1 plugin %d: %+v", i, p)
		}
	}
	if g.Plugins[11].Opt != "a,,b" {
		t.Errorf("empty opt entry: %q", g.Plugins[11].Opt)
	}
	m = g.Managed
	wantOverrides := fmt.Sprint([]Override{{FileOption: "java_multiple_files", Value: "true"}, {FileOption: "java_package", Value: "com.acme.a", Path: "acme/v1/a.proto"}})
	wantForms := fmt.Sprint([]Form{
		{Option: "optimize_for", Default: "SPEED", Except: []string{"buf.build/acme/slow"}},
		{Option: "java_package_prefix", Default: "com.acme"},
		{Option: "go_package_prefix", Default: "github.com/acme/gen", Except: []string{"buf.build/googleapis/googleapis"}, Override: map[string]string{"buf.build/acme/x": "github.com/acme/x"}},
		{Option: "objc_class_prefix", Default: "ACM"},
		{Option: "ruby_package"},
	})
	if m == nil || !m.Enabled || fmt.Sprint(m.Overrides) != wantOverrides || fmt.Sprint(m.Forms) != wantForms || join(g.Unmodeled) != "managed.ruby_package.default" {
		t.Fatalf("v1 managed: %+v %v", m, g.Unmodeled)
	}
	for name, c := range map[string]struct{ in, want string }{
		"two forms":        {"version: v2\nplugins:\n  - remote: buf.build/x/y\n    local: z\n    out: o\n", "buf.gen.yaml: plugins[0] names 2 plugin forms"},
		"v1 two forms":     {"version: v1\nplugins:\n  - plugin: buf.build/x/y\n    name: go\n    out: o\n", "names 2 plugin forms"},
		"no form":          {"version: v2\nplugins:\n  - out: o\n", "names 0 plugin forms"},
		"name reference":   {"version: v1\nplugins:\n  - name: buf.build/x/y\n    out: o\n", "is a plugin reference"},
		"v1 remote path":   {"version: v1\nplugins:\n  - plugin: buf.build/x/y:v1\n    path: p\n    out: o\n", "a remote plugin takes no path"},
		"remote strategy":  {"version: v2\nplugins:\n  - remote: buf.build/x/y\n    strategy: all\n    out: o\n", "a remote plugin takes no strategy"},
		"local revision":   {"version: v2\nplugins:\n  - local: z\n    revision: 1\n    out: o\n", "a local plugin takes no revision"},
		"local protoc":     {"version: v2\nplugins:\n  - local: z\n    protoc_path: p\n    out: o\n", "a local plugin takes no protoc_path"},
		"builtin revision": {"version: v2\nplugins:\n  - protoc_builtin: cpp\n    revision: 1\n    out: o\n", "a builtin plugin takes no revision"},
		"empty command":    {"version: v1\nplugins:\n  - name: go\n    path: []\n    out: o\n", "names no command"},
		"empty local":      {"version: v2\nplugins:\n  - local: \"\"\n    out: o\n", "names no command"},
		"empty plugin":     {"version: v1\nplugins:\n  - plugin: \"\"\n    out: o\n", "plugins[0].plugin names no plugin"},
		"empty remote":     {"version: v2\nplugins:\n  - remote: \"\"\n    out: o\n", "plugins[0].remote names no plugin"},
		"disable mapping":  {"version: v2\nmanaged:\n  disable:\n    - module: {a: b}\n", "module must be a scalar"},
		"disable empty":    {"version: v2\nmanaged:\n  disable:\n    - {}\n", "managed.disable[0] is empty"},
		"disable both":     {"version: v2\nmanaged:\n  disable:\n    - file_option: go_package\n      field_option: jstype\n", "names both file_option and field_option"},
		"disable field":    {"version: v2\nmanaged:\n  disable:\n    - file_option: go_package\n      field: f\n", "scopes a file_option to a field"},
		"override no opt":  {"version: v2\nmanaged:\n  override:\n    - path: x\n      value: v\n", "managed.override[0] names no file_option or field_option"},
		"override both":    {"version: v2\nmanaged:\n  override:\n    - file_option: go_package\n      field_option: jstype\n      value: v\n", "names both file_option and field_option"},
		"override no val":  {"version: v2\nmanaged:\n  override:\n    - file_option: go_package\n", "managed.override[0] has no value"},
		"unknown option":   {"version: v2\nmanaged:\n  override:\n    - file_option: go_pkg\n      value: v\n", "\"go_pkg\" is no option buf knows"},
		"unknown disable":  {"version: v2\nmanaged:\n  disable:\n    - field_option: ctype\n", "\"ctype\" is no option buf knows"},
		"v1 unknown opt":   {"version: v1\nmanaged:\n  override:\n    NOT_AN_OPTION:\n      a.proto: x\n", "managed.override \"not_an_option\" is no option buf knows"},
		"v1 spaced opt":    {"version: v1\nmanaged:\n  override:\n    \" JAVA_PACKAGE \":\n      a.proto: x\n", "\" java_package \" is no option buf knows"},
		"quoted bool":      {"version: v1\nmanaged:\n  enabled: \"true\"\n", "enabled must be true or false"},
		"override field":   {"version: v2\nmanaged:\n  override:\n    - file_option: go_package\n      field: f\n      value: v\n", "scopes a file_option to a field"},
		"v1 builtin path":  {"version: v1\nplugins:\n  - plugin: buf.build/x/y\n    protoc_path: p\n    out: o\n", "a remote plugin takes no protoc_path"},
		"v1 scalar map":    {"version: v1\nmanaged:\n  ruby_package: Acme\n", "ruby_package must be a mapping"},
		"v1 form scalar":   {"version: v1\nmanaged:\n  go_package_prefix: x\n", "go_package_prefix must be a mapping"},
		"v1 bool text":     {"version: v1\nmanaged:\n  cc_enable_arenas: yes\n", "cc_enable_arenas must be true or false"},
		"no out":           {"version: v2\nplugins:\n  - remote: buf.build/x/y\n", "has no out"},
		"enabled text":     {"version: v2\nmanaged:\n  enabled: yes\n", "enabled must be true or false"},
		"v1 override":      {"version: v1\nmanaged:\n  override:\n    JAVA_PACKAGE: x\n", "must be a mapping from file to value"},
	} {
		if _, err := ParseGen([]byte(c.in)); err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// v1's alpha remote plugin, `remote` alone: no form pb runs, the
// entry read for its out and opt and the key passed over (as
// REQ-migrate-gen has it); with a form beside it, buf's refusal.
func TestParseGenAlphaRemote(t *testing.T) {
	g, err := ParseGen([]byte("version: v1\nplugins:\n  - remote: buf.build/protocolbuffers/plugins/go:v1.28.1-1\n    out: gen/go\n    opt: paths=source_relative\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := g.Plugins[0]
	if p.Remote != "" || p.Local != nil || p.ProtocBuiltin != "" || p.Out != "gen/go" || p.Opt != "paths=source_relative" || join(p.Unmodeled) != "plugins[0].remote" {
		t.Fatalf("alpha remote: %+v", p)
	}
	if _, err := ParseGen([]byte("version: v1\nplugins:\n  - remote: buf.build/protocolbuffers/plugins/go:v1.28.1-1\n")); err == nil {
		t.Fatal("an alpha remote without out parsed")
	}
	if _, err := ParseGen([]byte("version: v1\nplugins:\n  - remote: buf.build/protocolbuffers/plugins/go:v1.28.1-1\n    out: gen\n    strategy: all\n")); err == nil || !strings.Contains(err.Error(), "a remote plugin takes no strategy") {
		t.Fatalf("an alpha remote with a key buf refuses: %v", err)
	}
	// A v2 remote is a plugin reference: an empty version, or a name
	// of two parts, does not parse.
	for _, remote := range []string{"buf.build/acme/x:", "buf.build/x:v1", "buf.build/acme/x:latest", "buf.build/acme/x:1.2.3", "buf.build/acme/x:abc123", "buf.build/acme/pl:ugin:v1.0.0", "buf.build/acme/x/y:v1"} {
		if _, err := ParseGen([]byte("version: v2\nplugins:\n  - remote: \"" + remote + "\"\n    out: gen\n")); err == nil || !strings.Contains(err.Error(), "is no plugin reference") {
			t.Errorf("%s: %v", remote, err)
		}
	}
	// What buf parses: a version buf's semver takes, a remote with a
	// port, an identity with no version.
	for _, remote := range []string{"buf.build/acme/x:v1.0.0", "buf.build/acme/x:v29.2", "bsr.example.com:8443/acme/plugin:v1.0.0", "bsr.example.com:8443/acme/plugin", "buf.build/acme/x"} {
		if _, err := ParseGen([]byte("version: v2\nplugins:\n  - remote: " + remote + "\n    out: gen\n")); err != nil {
			t.Errorf("%s: %v", remote, err)
		}
	}
	// swift_prefix is a file option buf knows, a v1 form with a
	// default and a v2 file_option.
	g, err = ParseGen([]byte("version: v1\nmanaged:\n  enabled: true\n  swift_prefix:\n    default: SWF\nplugins:\n  - name: go\n    out: gen\n"))
	if err != nil || len(g.Managed.Forms) != 1 || g.Managed.Forms[0].Option != "swift_prefix" || g.Managed.Forms[0].Default != "SWF" {
		t.Fatalf("v1 swift_prefix: %+v %v", g.Managed, err)
	}
	g, err = ParseGen([]byte("version: v2\nmanaged:\n  enabled: true\n  override:\n    - file_option: swift_prefix\n      value: SWF\nplugins:\n  - local: gen\n    out: gen\n"))
	if err != nil || len(g.Managed.Overrides) != 1 || g.Managed.Overrides[0].FileOption != "swift_prefix" {
		t.Fatalf("v2 swift_prefix: %+v %v", g.Managed, err)
	}
}
