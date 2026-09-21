package migrate

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/plugin/genfile"
)

// specRows reads a three-column table of backticked cells under one
// heading of a spec, the third column's cells space-separated.
func specRows(t *testing.T, file, heading string) map[string][2]string {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile("(?s)\\n## " + regexp.QuoteMeta(heading) + "\\n(.*?)(\\n## |$)").FindStringSubmatch(string(text))
	if section == nil {
		t.Fatalf("%s has no section %q", file, heading)
	}
	rows := map[string][2]string{}
	for _, r := range regexp.MustCompile("(?m)^\\| `([^`]+)` \\| `([^`]+)` \\| ((?:`[^`]+` ?)+) \\|$").FindAllStringSubmatch(section[1], -1) {
		rows[r[1]] = [2]string{r[2], strings.TrimSpace(strings.ReplaceAll(r[3], "`", ""))}
	}
	return rows
}

// tagLess orders buf's plugin tags, `v` and dot-separated numbers
// (v29.2, v1.34.2): by number, component by component, a shorter tag
// equal so far the lesser.
func tagLess(a, b string) bool {
	as, bs := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		var x, y int
		fmt.Sscan(as[i], &x)
		fmt.Sscan(bs[i], &y)
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

// The plugin table is the spec's table, entry for entry, each
// repository a plugin reference's repository, each version a tag the
// reference grammar accepts, the versions ascending.
func TestPluginTableMatchesSpec(t *testing.T) {
	rows := specRows(t, "../../docs/specs/migrate.md", "Generation")
	if len(rows) != len(Plugins) || len(rows) == 0 {
		t.Fatalf("the spec's table has %d rows, the code's %d", len(rows), len(Plugins))
	}
	for name, row := range rows {
		e, ok := Plugins[name]
		if !ok || e.repo != row[0] || strings.Join(e.versions, " ") != row[1] {
			t.Errorf("%s: spec %v, code %+v", name, row, e)
		}
		for i, v := range e.versions {
			if err := genfile.CheckReference(e.repo + ":" + v); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if i > 0 && tagLess(v, e.versions[i-1]) || i > 0 && v == e.versions[i-1] {
				t.Errorf("%s: %s follows %s, not ascending", name, v, e.versions[i-1])
			}
		}
	}
}

// Every image the plugin table names is reachable at its tag: the
// registry answers a manifest for each. Runs only where
// PB_LIVE_IMAGES is set — at an entry's addition.
func TestPluginImages(t *testing.T) {
	if os.Getenv("PB_LIVE_IMAGES") == "" {
		t.Skip("PB_LIVE_IMAGES unset: the registry is not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, k := range sortedKeys(Plugins) {
		e := Plugins[k]
		for _, v := range e.versions {
			ref, err := name.ParseReference(e.repo + ":" + v)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := remote.Head(ref, remote.WithContext(ctx)); err != nil {
				t.Errorf("%s: %v", ref, err)
			}
		}
	}
}

func parseGen(t *testing.T, text string) *bufconfig.Gen {
	t.Helper()
	g, err := bufconfig.ParseGen([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The generation file from buf.gen.yaml (REQ-migrate-gen): BSR
// plugins through the replacements and the table at the version named
// or the highest built, local executables as local entries, the
// forms pb does not run unmapped, the keys pb has no meaning for
// unmapped naming them, an out pb writes nowhere unmapped; managed
// mode's declarative entries as overrides, a path as buf matches it
// against a file's module-relative path, its heuristics and module
// scopes unmapped.
func TestGen(t *testing.T) {
	v2 := parseGen(t, `version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/acme/gen
    - file_option: java_package
      value: com.acme
      path: acme/v1
    - file_option: java_multiple_files
      value: "true"
    - field_option: jstype
      value: JS_STRING
    - file_option: csharp_namespace
      value: Acme
      module: buf.build/acme/petapis
    - file_option: ruby_package_suffix
      value: Proto
    - file_option: objc_class_prefix
      value: ACM
      path: .
    - file_option: php_namespace
      value: Acme
      path: ../up
    - file_option: swift_prefix
      value: ACM
      path: v1[beta]
  disable:
    - file_option: go_package
      module: buf.build/googleapis/googleapis
plugins:
  - remote: buf.build/protocolbuffers/go:v1.35.2
    out: gen/go
    opt: paths=source_relative
    include_imports: true
  - remote: buf.build/grpc/go
    out: gen/go
  - remote: buf.build/protocolbuffers/go:v1.36.0
    out: gen/go36
  - remote: buf.build/acme/custom:v1.0.0
    out: gen/custom
  - remote: buf.build/acme/replaced:v2
    out: gen/replaced
  - local: protoc-gen-connect-go
    out: gen/connect
    opt: [paths=source_relative, package_suffix=]
  - local: [go, run, ./cmd/gen]
    out: gen/x
  - protoc_builtin: cpp
    out: gen/cpp
  - local: protoc-gen-up
    out: ../up
inputs:
  - directory: proto
`)
	l := &Layout{}
	var repl Replacements
	if err := repl.Replace("plugin", "buf.build/acme/replaced=ghcr.io/acme/protoc-gen-replaced:v2.0.0"); err != nil {
		t.Fatal(err)
	}
	facts, err := Gen(v2, repl, l)
	if err != nil {
		t.Fatal(err)
	}
	pbgo := "ghcr.io/greatliontech/pbr-plugins/protocolbuffers/go"
	heuristic := " !! buf's own heuristic, a value computed per file from its package: pb declares values alone"
	want := strings.Join([]string{
		"buf.gen.yaml plugins[0].remote buf.build/protocolbuffers/go:v1.35.2 -> ref: " + pbgo + ":v1.35.2 (the plugin table)",
		"buf.gen.yaml plugins[0].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[0].opt paths=source_relative -> opt: paths=source_relative",
		"buf.gen.yaml plugins[0].include_imports !! pb generates over the workspace's own files under one strategy",
		"buf.gen.yaml plugins[1].remote buf.build/grpc/go -> ref: ghcr.io/greatliontech/pbr-plugins/grpc/go:v1.5.1 (the plugin table; no version named, the highest the fork builds)",
		"buf.gen.yaml plugins[1].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[2].remote buf.build/protocolbuffers/go:v1.36.0 !! the fork builds v1.34.2, v1.35.2, not v1.36.0: pass --plugin buf.build/protocolbuffers/go=<reference>",
		"buf.gen.yaml plugins[3].remote buf.build/acme/custom:v1.0.0 !! no entry in the plugin table: pass --plugin buf.build/acme/custom=<reference>",
		"buf.gen.yaml plugins[4].remote buf.build/acme/replaced:v2 -> ref: ghcr.io/acme/protoc-gen-replaced:v2.0.0 (--plugin)",
		"buf.gen.yaml plugins[4].out gen/replaced -> out: gen/replaced",
		"buf.gen.yaml plugins[5].local protoc-gen-connect-go -> local: protoc-gen-connect-go",
		"buf.gen.yaml plugins[5].out gen/connect -> out: gen/connect",
		"buf.gen.yaml plugins[5].opt paths=source_relative,package_suffix= -> opt: paths=source_relative,package_suffix=",
		"buf.gen.yaml plugins[6].local go run ./cmd/gen !! a command with arguments: pb runs an executable alone",
		"buf.gen.yaml plugins[7].protoc_builtin cpp !! pb runs no protoc",
		"buf.gen.yaml plugins[8].local protoc-gen-up -> local: protoc-gen-up",
		"buf.gen.yaml plugins[8].out ../up !! pb writes within the resolution root: \"../up\" escapes the resolution root",
		"buf.gen.yaml inputs !! pb generates over the workspace's own files",
		"buf.gen.yaml managed.override[0] file_option=go_package_prefix value=github.com/acme/gen" + heuristic,
		"buf.gen.yaml managed.override[1] file_option=java_package path=acme/v1 -> overrides: files acme/v1/** option java_package value com.acme",
		"buf.gen.yaml managed.override[2] file_option=java_multiple_files -> overrides: files ** option java_multiple_files value true",
		"buf.gen.yaml managed.override[3] field_option=jstype !! a field option: pb's overrides are file options",
		"buf.gen.yaml managed.override[4] file_option=csharp_namespace module=buf.build/acme/petapis !! pb's override files are module-relative globs, naming no module",
		"buf.gen.yaml managed.override[5] file_option=ruby_package_suffix value=Proto" + heuristic,
		"buf.gen.yaml managed.override[6] file_option=objc_class_prefix path=. -> overrides: files ** option objc_class_prefix value ACM",
		"buf.gen.yaml managed.override[7] file_option=php_namespace path=../up !! no path buf matches: \"../up\" escapes the module",
		"buf.gen.yaml managed.override[8] file_option=swift_prefix path=v1[beta] -> overrides: files v1\\[beta\\]/** option swift_prefix value ACM",
		"buf.gen.yaml managed.disable[0] file_option=go_package module=buf.build/googleapis/googleapis !! buf's own heuristic: pb declares values alone, disabling nothing",
	}, "\n")
	if got := factsOf(facts); got != want {
		t.Fatalf("v2 facts:\n%s", got)
	}
	out, err := genfile.Encode(l.Gen)
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "plugins:\n  - ref: " + pbgo + ":v1.35.2\n    out: gen/go\n    opt: paths=source_relative\n" +
		"  - ref: ghcr.io/greatliontech/pbr-plugins/grpc/go:v1.5.1\n    out: gen/go\n" +
		"  - ref: ghcr.io/acme/protoc-gen-replaced:v2.0.0\n    out: gen/replaced\n" +
		"  - local: protoc-gen-connect-go\n    out: gen/connect\n    opt: paths=source_relative,package_suffix=\n" +
		"overrides:\n  - files: acme/v1/**\n    option: java_package\n    value: com.acme\n  - files: \"**\"\n    option: java_multiple_files\n    value: \"true\"\n  - files: \"**\"\n    option: objc_class_prefix\n    value: ACM\n  - files: v1\\[beta\\]/**\n    option: swift_prefix\n    value: ACM\n"
	if string(out) != wantFile {
		t.Fatalf("v2 file:\n%s", out)
	}

	// v1: booleans, the override map's files, the declared defaults of
	// optimize_for and objc_class_prefix; per-module and per-package
	// forms unmapped, each module named; the alpha remote key unmapped.
	v1 := parseGen(t, `version: v1
managed:
  enabled: true
  java_multiple_files: true
  optimize_for:
    default: SPEED
    except: [buf.build/acme/slow]
    override:
      buf.build/acme/fast: CODE_SIZE
  java_package_prefix: com.acme
  go_package_prefix:
    default: github.com/acme/gen
  objc_class_prefix:
    default: ACM
    except: [buf.build/acme/other]
  swift_prefix:
    default: SWF
  csharp_namespace:
    override:
      buf.build/acme/x: Acme.X
  override:
    JAVA_PACKAGE:
      acme/v1/a.proto: com.acme.a
plugins:
  - plugin: buf.build/protocolbuffers/go:v1.34.2
    out: gen/go
  - name: go-grpc
    out: gen/go
  - plugin: cpp
    out: gen/cpp
  - remote: buf.build/protocolbuffers/plugins/go:v1.28.1-1
    out: gen/alpha
`)
	l = &Layout{}
	facts, err = Gen(v1, Replacements{}, l)
	if err != nil {
		t.Fatal(err)
	}
	want = strings.Join([]string{
		"buf.gen.yaml plugins[0].remote buf.build/protocolbuffers/go:v1.34.2 -> ref: " + pbgo + ":v1.34.2 (the plugin table)",
		"buf.gen.yaml plugins[0].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[1].local protoc-gen-go-grpc -> local: protoc-gen-go-grpc",
		"buf.gen.yaml plugins[1].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[2].protoc_builtin cpp !! pb runs no protoc",
		"buf.gen.yaml plugins[3].remote !! buf's alpha remote plugin, run by the BSR: pb runs every plugin itself",
		"buf.gen.yaml managed.override[0] file_option=java_multiple_files -> overrides: files ** option java_multiple_files value true",
		"buf.gen.yaml managed.override[1] file_option=java_package path=acme/v1/a.proto -> overrides: files acme/v1/a.proto/** option java_package value com.acme.a",
		"buf.gen.yaml managed.optimize_for.default SPEED -> overrides: files ** option optimize_for value SPEED",
		"buf.gen.yaml managed.optimize_for.except buf.build/acme/slow !! names a module, which a module-relative glob cannot",
		"buf.gen.yaml managed.optimize_for.override buf.build/acme/fast=CODE_SIZE !! names a module, which a module-relative glob cannot",
		"buf.gen.yaml managed.java_package_prefix com.acme" + heuristic,
		"buf.gen.yaml managed.go_package_prefix github.com/acme/gen" + heuristic,
		"buf.gen.yaml managed.objc_class_prefix.default ACM -> overrides: files ** option objc_class_prefix value ACM",
		"buf.gen.yaml managed.objc_class_prefix.except buf.build/acme/other !! names a module, which a module-relative glob cannot",
		"buf.gen.yaml managed.swift_prefix.default SWF -> overrides: files ** option swift_prefix value SWF",
		"buf.gen.yaml managed.csharp_namespace (no default)" + heuristic,
		"buf.gen.yaml managed.csharp_namespace.override buf.build/acme/x=Acme.X !! names a module, which a module-relative glob cannot",
	}, "\n")
	if got := factsOf(facts); got != want {
		t.Fatalf("v1 facts:\n%s", got)
	}
	out, err = genfile.Encode(l.Gen)
	if err != nil {
		t.Fatal(err)
	}
	wantFile = "plugins:\n  - ref: " + pbgo + ":v1.34.2\n    out: gen/go\n  - local: protoc-gen-go-grpc\n    out: gen/go\n" +
		"overrides:\n  - files: \"**\"\n    option: java_multiple_files\n    value: \"true\"\n  - files: acme/v1/a.proto/**\n    option: java_package\n    value: com.acme.a\n" +
		"  - files: \"**\"\n    option: optimize_for\n    value: SPEED\n  - files: \"**\"\n    option: objc_class_prefix\n    value: ACM\n  - files: \"**\"\n    option: swift_prefix\n    value: SWF\n"
	if string(out) != wantFile {
		t.Fatalf("v1 file:\n%s", out)
	}

	// Managed mode disabled reads for nothing; enabled with nothing
	// explicit is buf's heuristic; no plugin mapped writes no file and
	// reads the managed mode for nothing.
	g := parseGen(t, "version: v2\nmanaged:\n  enabled: false\n  override:\n    - file_option: java_package\n      value: x\nplugins:\n  - local: gen\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(g, Replacements{}, l)
	if err != nil || l.Gen == nil || len(l.Gen.Overrides) != 0 || !strings.Contains(factsOf(facts), "buf.gen.yaml managed.enabled false -> nothing: managed mode is disabled") {
		t.Fatalf("disabled: %v %+v\n%s", err, l.Gen, factsOf(facts))
	}
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: x\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(g, Replacements{}, l)
	if got := factsOf(facts); err != nil || l.Gen != nil || strings.Contains(got, "overrides:") || !strings.Contains(got, "buf.gen.yaml plugins !! no plugin mapped: no generation file is written\nbuf.gen.yaml managed !! no generation file is written, no override with it") {
		t.Fatalf("nothing mapped: %v %+v\n%s", err, l.Gen, got)
	}
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\nplugins:\n  - local: gen\n    out: gen\n")
	facts, err = Gen(g, Replacements{}, &Layout{})
	if err != nil || !strings.Contains(factsOf(facts), "buf.gen.yaml managed.enabled true !! enabled with no explicit override: buf's own heuristic") {
		t.Fatalf("enabled alone: %v %s", err, factsOf(facts))
	}
	// With no plugin mapped, a disabled or empty managed mode is what
	// it would have been.
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: false\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	facts, err = Gen(g, Replacements{}, &Layout{})
	if got := factsOf(facts); err != nil || !strings.HasSuffix(got, "buf.gen.yaml plugins !! no plugin mapped: no generation file is written\nbuf.gen.yaml managed.enabled false -> nothing: managed mode is disabled, its entries read for nothing") {
		t.Fatalf("disabled, nothing mapped: %v\n%s", err, got)
	}

	// Replacements naming no plugin of the file are refused together;
	// one that is no reference is refused; a remote naming a port is
	// split at its ref, not its port.
	var unusedRepl Replacements
	for _, r := range []string{"buf.build/acme/u1=ghcr.io/a/b:v1", "buf.build/acme/u0=ghcr.io/a/c:v1"} {
		if err := unusedRepl.Replace("plugin", r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Gen(g, unusedRepl, l); err == nil || err.Error() != "--plugin buf.build/acme/u0, --plugin buf.build/acme/u1: buf.gen.yaml names no such plugin" {
		t.Fatalf("unused replacements: %v", err)
	}
	var badRepl Replacements
	if err := badRepl.Replace("plugin", "buf.build/nobody/knows=not-a-reference"); err != nil {
		t.Fatal(err)
	}
	g = parseGen(t, "version: v2\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	if _, err := Gen(g, badRepl, l); err == nil || !strings.Contains(err.Error(), "--plugin buf.build/nobody/knows=not-a-reference") {
		t.Fatalf("a replacement that is no reference: %v", err)
	}
	if name, ref := bsrSplit("bsr.example.com:8443/acme/plugin:v1"); name != "bsr.example.com:8443/acme/plugin" || ref != "v1" {
		t.Fatalf("bsrSplit: %q %q", name, ref)
	}
	if name, ref := bsrSplit("bsr.example.com:8443/acme/plugin"); name != "bsr.example.com:8443/acme/plugin" || ref != "" {
		t.Fatalf("bsrSplit without a ref: %q %q", name, ref)
	}
	if _, err := Gen(nil, Replacements{}, nil); err == nil {
		t.Fatal("no file: no error")
	}
}
