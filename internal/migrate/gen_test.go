package migrate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/pb/internal/provenance/image/discover"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// specNames reads the backticked `buf.build/` names listed under one
// heading of a spec document.
func specNames(t *testing.T, file, heading string) []string {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile("(?s)\\n## " + regexp.QuoteMeta(heading) + "\\n(.*?)(\\n## |$)").FindStringSubmatch(string(text))
	if section == nil {
		t.Fatalf("%s has no section %q", file, heading)
	}
	var names []string
	for _, m := range regexp.MustCompile("(?m)^- `(buf\\.build/[^`]+)`$").FindAllStringSubmatch(section[1], -1) {
		names = append(names, m[1])
	}
	return names
}

// The plugin catalog is the spec's list, name for name and in order,
// no name twice, each name buf's `buf.build/<owner>/<plugin>` whose
// repository under the rename rule is one a plugin reference's
// grammar accepts.
func TestPluginCatalogMatchesSpec(t *testing.T) {
	names := specNames(t, "../../docs/specs/migrate.md", "Generation")
	if strings.Join(names, " ") != strings.Join(Catalog, " ") || len(names) == 0 {
		t.Fatalf("the spec lists %v, the code %v", names, Catalog)
	}
	for i := 1; i < len(Catalog); i++ {
		if Catalog[i-1] >= Catalog[i] {
			t.Errorf("the catalog is not strictly sorted at %s, %s", Catalog[i-1], Catalog[i])
		}
	}
	for _, n := range Catalog {
		if !strings.HasPrefix(n, "buf.build/") || strings.Count(n, "/") != 2 {
			t.Errorf("%s: not buf.build/<owner>/<plugin>", n)
		}
		if err := genfile.CheckReference(CatalogRepository(n) + ":v1.0.0"); err != nil {
			t.Errorf("%s: %v", n, err)
		}
		if !inCatalog(n) || inCatalog(n+"x") || inCatalog("buf.build/"+n) {
			t.Errorf("%s: membership", n)
		}
	}
	if got := CatalogRepository("buf.build/grpc/go"); got != "ghcr.io/greatliontech/pb-plugins/grpc/go" {
		t.Errorf("rename: %s", got)
	}
}

// The highest version tag is the highest by number, component by
// component, among the tags spelled as versions alone; tags of
// another shape — a signature tag, `latest`, a digest — are passed
// over, and a listing without a version tag yields none.
func TestHighestVersionTag(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"v1.2.3", "v1.10.0", "v1.9.9", "sha256-ab.sig", "latest", "v2", "1.0.0", "v1.10.0-rc1"}, "v1.10.0"},
		{[]string{"v36.2", "v36.10", "v36.1.1"}, "v36.10"},
		{[]string{"v1.10.0", "v1.11.0-rc1", "v1.12.0+build"}, "v1.10.0"},
		{[]string{"v1.010.0", "v1.9.0", "v1.08.0", "v01.20.0"}, "v1.9.0"},
		{[]string{"v1.99999999999999999999", "v1.5", "v1.100000000000000000000"}, "v1.100000000000000000000"},
		{[]string{"v0.1", "v0.0.1", "v0"}, "v0.1"},
		{[]string{"v1.2", "v1.2.0"}, "v1.2.0"},
		{[]string{"latest", "sha256-ab.sig"}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := highestVersionTag(tc.tags); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.tags, got, tc.want)
		}
	}
	if !tagLess("v1.35.2", "v1.36.0") || tagLess("v1.36.0", "v1.35.2") || !tagLess("v29", "v29.2") || tagLess("v29.2", "v29.2") || !tagLess("v1.9", "v1.10") || tagLess("v1.10", "v1.9") {
		t.Error("tagLess")
	}
}

// catalogCommit pins the catalog repository's commit the copy is
// held to; it moves when the copy changes, to the earliest catalog
// commit the copy then equals.
const catalogCommit = "49d39d954c86738e6ff28219fa7cb2e1973aa98f"

// The catalog's identity: the publish workflow's, as migrate.md
// states it for the trust policy rule.
const (
	catalogSAN    = "https://github.com/greatliontech/pb-plugins/.github/workflows/publish.yaml@refs/heads/main"
	catalogIssuer = "https://token.actions.githubusercontent.com"
)

// The catalog copy is the repository's catalog at the pinned commit,
// name for name, and each name's highest published tag names a list
// the registry answers, signed under the catalog's identity. Runs
// only where PB_LIVE_IMAGES is set — at an entry's addition — with
// GITHUB_TOKEN, where set, raising the API's rate limit, the
// registry's credentials in the ambient store, and PBTRUSTEDROOT naming the
// trusted root the signatures are verified against; its budget is
// the binary's `-timeout`, sized to the list or lifted.
func TestPluginCatalog(t *testing.T) {
	if os.Getenv("PB_LIVE_IMAGES") == "" {
		t.Skip("PB_LIVE_IMAGES unset: the registry is not reached")
	}
	// The budget is the test binary's own (`-timeout`), which the run
	// sizes to the list — each name's listing and signature take
	// seconds — or lifts (`-timeout 0`, no deadline at all).
	ctx, cancel := context.WithCancel(context.Background())
	if deadline, ok := t.Deadline(); ok {
		cancel()
		ctx, cancel = context.WithDeadline(context.Background(), deadline.Add(-10*time.Second))
	}
	defer cancel()
	root, err := gitprov.LoadTrustedRoot(os.Getenv("PBTRUSTEDROOT"))
	if err != nil {
		t.Fatalf("PBTRUSTEDROOT: %v", err)
	}
	id, err := trust.ExplicitIdentity(trust.IdentityRule{SAN: catalogSAN, Issuer: catalogIssuer})
	if err != nil {
		t.Fatal(err)
	}
	// The repository's catalog at the pinned commit.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/greatliontech/pb-plugins/contents/catalog.yaml?ref="+catalogCommit, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the catalog at %s: %s %v", catalogCommit, resp.Status, err)
	}
	var doc struct {
		Registry string                    `yaml:"registry"`
		Plugins  map[string]map[string]any `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Registry != CatalogRegistry {
		t.Errorf("the catalog publishes under %s, the copy names %s", doc.Registry, CatalogRegistry)
	}
	var names []string
	for n := range doc.Plugins {
		names = append(names, "buf.build/"+n)
	}
	sort.Strings(names)
	if strings.Join(names, " ") != strings.Join(Catalog, " ") {
		t.Errorf("the catalog lists %v, the copy %v", names, Catalog)
	}
	// Each name's highest tag: reachable and signed.
	keychain := remote.WithAuthFromKeychain(authn.DefaultKeychain)
	for _, n := range Catalog {
		repo, err := name.NewRepository(CatalogRepository(n))
		if err != nil {
			t.Fatal(err)
		}
		tags, err := remote.List(repo, remote.WithContext(ctx), keychain)
		if err != nil {
			t.Errorf("%s: %v", repo, err)
			continue
		}
		highest := highestVersionTag(tags)
		if highest == "" {
			t.Errorf("%s: no version tag among %v", repo, tags)
			continue
		}
		desc, err := remote.Head(repo.Tag(highest), remote.WithContext(ctx), keychain)
		if err != nil {
			t.Errorf("%s:%s: %v", repo, highest, err)
			continue
		}
		if !desc.MediaType.IsIndex() {
			t.Errorf("%s:%s: %s is no list", repo, highest, desc.MediaType)
		}
		h, err := v1.NewHash(desc.Digest.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := image.Judge(ctx, desc.Digest.String(), discover.Discover(ctx, repo, h, keychain), id, root, nil); err != nil {
			t.Errorf("%s:%s@%s: %v", repo, highest, desc.Digest, err)
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
    include_wkt: true
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
	l := &Layout{Names: map[string]string{"buf.build/acme/petapis": "example.com/acme/petapis"}, DepPaths: map[string]string{"buf.build/googleapis/googleapis": "github.com/googleapis/googleapis"}}
	var repl Replacements
	if err := repl.Replace("plugin", "buf.build/acme/replaced=ghcr.io/acme/protoc-gen-replaced:v2.0.0"); err != nil {
		t.Fatal(err)
	}
	tags := fakeTags{CatalogRepository("buf.build/grpc/go"): {"latest", "v1.5.1", "v1.6.2", "sha256-ab.sig"}}
	facts, err := Gen(context.Background(), v2, nil, repl, l, tags.list, nil)
	if err != nil {
		t.Fatal(err)
	}
	pbgo := "ghcr.io/greatliontech/pb-plugins/protocolbuffers/go"
	want := strings.Join([]string{
		"buf.gen.yaml plugins[0].remote buf.build/protocolbuffers/go:v1.35.2 -> ref: " + pbgo + ":v1.35.2 (the catalog)",
		"buf.gen.yaml plugins[0].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[0].opt paths=source_relative -> opt: paths=source_relative",
		"buf.gen.yaml plugins[0].include_imports true -> include_imports: true",
		"buf.gen.yaml plugins[0].include_wkt true -> include_wkt: true",
		"buf.gen.yaml plugins[1].remote buf.build/grpc/go -> ref: ghcr.io/greatliontech/pb-plugins/grpc/go:v1.6.2 (the catalog; no version named, the highest tag published)",
		"buf.gen.yaml plugins[1].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[2].remote buf.build/protocolbuffers/go:v1.36.0 -> ref: " + pbgo + ":v1.36.0 (the catalog)",
		"buf.gen.yaml plugins[2].out gen/go36 -> out: gen/go36",
		"buf.gen.yaml plugins[3].remote buf.build/acme/custom:v1.0.0 !! not in the plugin catalog: pass --plugin buf.build/acme/custom=<reference>",
		"buf.gen.yaml plugins[4].remote buf.build/acme/replaced:v2 -> ref: ghcr.io/acme/protoc-gen-replaced:v2.0.0 (--plugin)",
		"buf.gen.yaml plugins[4].out gen/replaced -> out: gen/replaced",
		"buf.gen.yaml plugins[5].local protoc-gen-connect-go -> local: protoc-gen-connect-go",
		"buf.gen.yaml plugins[5].out gen/connect -> out: gen/connect",
		"buf.gen.yaml plugins[5].opt paths=source_relative,package_suffix= -> opt: paths=source_relative,package_suffix=",
		"buf.gen.yaml plugins[6].local go run ./cmd/gen -> local: go run ./cmd/gen",
		"buf.gen.yaml plugins[6].out gen/x -> out: gen/x",
		"buf.gen.yaml plugins[7].protoc_builtin cpp !! pb runs no protoc",
		"buf.gen.yaml plugins[8].local protoc-gen-up -> local: protoc-gen-up",
		"buf.gen.yaml plugins[8].out ../up !! pb writes within the resolution root: ../up escapes it",
		"buf.gen.yaml inputs[0].directory proto !! under no workspace module: pb generates over the modules' own files",
		"buf.gen.yaml managed.override[3] field_option=jstype !! a field option: pb's overrides are file options",
		"buf.gen.yaml managed.override[7] file_option=php_namespace path=../up !! no path buf matches: \"../up\" escapes the module",
		"buf.gen.yaml managed.disable[0] file_option=go_package module=buf.build/googleapis/googleapis -> the module github.com/googleapis/googleapis sees go_package without buf.gen.yaml managed.override[0] file_option=go_package_prefix value=github.com/acme/gen for go_package: its overrides of the option recomputed, every module's excepting it",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option cc_enable_arenas value true (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option csharp_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_multiple_files value true (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_outer_classname (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_package prefix com (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option objc_class_prefix (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option php_metadata_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option php_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option ruby_package (buf's default)",
		"buf.gen.yaml managed.override[0] file_option=go_package_prefix value=github.com/acme/gen -> overrides: files ** option go_package prefix github.com/acme/gen",
		"buf.gen.yaml managed.override[1] file_option=java_package path=acme/v1 -> overrides: files acme/v1/** option java_package value com.acme",
		"buf.gen.yaml managed.override[2] file_option=java_multiple_files -> overrides: files ** option java_multiple_files value true",
		"buf.gen.yaml managed.override[2] file_option=java_multiple_files -> replacing buf.gen.yaml managed.enabled true's override of java_multiple_files for files **",
		"buf.gen.yaml managed.override[4] file_option=csharp_namespace module=buf.build/acme/petapis -> overrides: files ** module example.com/acme/petapis option csharp_namespace value Acme",
		"buf.gen.yaml managed.override[5] file_option=ruby_package_suffix value=Proto -> overrides: files ** option ruby_package suffix Proto (after buf.gen.yaml managed.enabled true, which set no prefix)",
		"buf.gen.yaml managed.override[5] file_option=ruby_package_suffix value=Proto -> replacing buf.gen.yaml managed.enabled true's override of ruby_package for files **",
		"buf.gen.yaml managed.override[6] file_option=objc_class_prefix path=. -> overrides: files ** option objc_class_prefix value ACM",
		"buf.gen.yaml managed.override[6] file_option=objc_class_prefix path=. -> replacing buf.gen.yaml managed.enabled true's override of objc_class_prefix for files **",
		"buf.gen.yaml managed.override[8] file_option=swift_prefix path=v1[beta] -> overrides: files v1\\[beta\\]/** option swift_prefix value ACM",
	}, "\n")
	if got := factsOf(facts); got != want {
		t.Fatalf("v2 facts:\n%s", got)
	}
	out, err := genfile.Encode(l.Gen)
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "plugins:\n  - ref: " + pbgo + ":v1.35.2\n    out: gen/go\n    opt: paths=source_relative\n    include_imports: true\n    include_wkt: true\n" +
		"  - ref: ghcr.io/greatliontech/pb-plugins/grpc/go:v1.6.2\n    out: gen/go\n" +
		"  - ref: " + pbgo + ":v1.36.0\n    out: gen/go36\n" +
		"  - ref: ghcr.io/acme/protoc-gen-replaced:v2.0.0\n    out: gen/replaced\n" +
		"  - local: protoc-gen-connect-go\n    out: gen/connect\n    opt: paths=source_relative,package_suffix=\n" +
		"  - local:\n      - go\n      - run\n      - ./cmd/gen\n    out: gen/x\n" +
		"overrides:\n  - files: \"**\"\n    option: cc_enable_arenas\n    value: \"true\"\n  - files: \"**\"\n    option: csharp_namespace\n  - files: \"**\"\n    option: java_outer_classname\n  - files: \"**\"\n    option: java_package\n    prefix: com\n" +
		"  - files: \"**\"\n    option: php_metadata_namespace\n  - files: \"**\"\n    option: php_namespace\n" +
		"  - files: \"**\"\n    except:\n      - github.com/googleapis/googleapis\n    option: go_package\n    prefix: github.com/acme/gen\n  - files: acme/v1/**\n    option: java_package\n    value: com.acme\n  - files: \"**\"\n    option: java_multiple_files\n    value: \"true\"\n" +
		"  - files: \"**\"\n    module: example.com/acme/petapis\n    option: csharp_namespace\n    value: Acme\n" +
		"  - files: \"**\"\n    option: ruby_package\n    suffix: Proto\n  - files: \"**\"\n    option: objc_class_prefix\n    value: ACM\n  - files: v1\\[beta\\]/**\n    option: swift_prefix\n    value: ACM\n"
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
	l = &Layout{Names: map[string]string{"buf.build/acme/x": "example.com/acme/x"}, DepPaths: map[string]string{"buf.build/acme/slow": "github.com/acme/slow", "buf.build/acme/fast": "github.com/acme/fast"}}
	facts, err = Gen(context.Background(), v1, nil, Replacements{}, l, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want = strings.Join([]string{
		"buf.gen.yaml plugins[0].remote buf.build/protocolbuffers/go:v1.34.2 -> ref: " + pbgo + ":v1.34.2 (the catalog)",
		"buf.gen.yaml plugins[0].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[1].local protoc-gen-go-grpc -> local: protoc-gen-go-grpc",
		"buf.gen.yaml plugins[1].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[2].protoc_builtin cpp !! pb runs no protoc",
		"buf.gen.yaml plugins[3].remote !! buf's alpha remote plugin, run by the BSR: pb runs every plugin itself",
		"buf.gen.yaml managed.objc_class_prefix.except buf.build/acme/other !! names the module buf.build/acme/other, which the configuration declares nowhere: no module path stands for it",
		"buf.gen.yaml managed.optimize_for.except buf.build/acme/slow -> the module github.com/acme/slow sees optimize_for without buf.gen.yaml managed.optimize_for.default SPEED for optimize_for: its overrides of the option recomputed, every module's excepting it",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option cc_enable_arenas value true (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option csharp_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_multiple_files value true (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_outer_classname (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_package prefix com (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option objc_class_prefix (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option php_metadata_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option php_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option ruby_package (buf's default)",
		"buf.gen.yaml managed.optimize_for.default SPEED -> overrides: files ** option optimize_for value SPEED",
		"buf.gen.yaml managed.optimize_for.override buf.build/acme/fast=CODE_SIZE -> overrides: files ** module github.com/acme/fast option optimize_for value CODE_SIZE",
		"buf.gen.yaml managed.java_package_prefix.default com.acme -> overrides: files ** option java_package prefix com.acme (after buf.gen.yaml managed.enabled true, which set no suffix)",
		"buf.gen.yaml managed.java_package_prefix.default com.acme -> replacing buf.gen.yaml managed.enabled true's override of java_package for files **",
		"buf.gen.yaml managed.go_package_prefix.default github.com/acme/gen -> overrides: files ** option go_package prefix github.com/acme/gen",
		"buf.gen.yaml managed.objc_class_prefix.default ACM -> overrides: files ** option objc_class_prefix value ACM",
		"buf.gen.yaml managed.objc_class_prefix.default ACM -> replacing buf.gen.yaml managed.enabled true's override of objc_class_prefix for files **",
		"buf.gen.yaml managed.swift_prefix.default SWF -> overrides: files ** option swift_prefix value SWF",
		"buf.gen.yaml managed.csharp_namespace.override buf.build/acme/x=Acme.X -> overrides: files ** module example.com/acme/x option csharp_namespace value Acme.X",
		"buf.gen.yaml managed.override[0] file_option=java_multiple_files -> overrides: files ** option java_multiple_files value true",
		"buf.gen.yaml managed.override[0] file_option=java_multiple_files -> replacing buf.gen.yaml managed.enabled true's override of java_multiple_files for files **",
		"buf.gen.yaml managed.override[1] file_option=java_package path=acme/v1/a.proto -> overrides: files acme/v1/a.proto/** option java_package value com.acme.a",
	}, "\n")
	if got := factsOf(facts); got != want {
		t.Fatalf("v1 facts:\n%s", got)
	}
	out, err = genfile.Encode(l.Gen)
	if err != nil {
		t.Fatal(err)
	}
	wantFile = "plugins:\n  - ref: " + pbgo + ":v1.34.2\n    out: gen/go\n  - local: protoc-gen-go-grpc\n    out: gen/go\n" +
		"overrides:\n  - files: \"**\"\n    option: cc_enable_arenas\n    value: \"true\"\n  - files: \"**\"\n    option: csharp_namespace\n  - files: \"**\"\n    option: java_outer_classname\n" +
		"  - files: \"**\"\n    option: php_metadata_namespace\n  - files: \"**\"\n    option: php_namespace\n  - files: \"**\"\n    option: ruby_package\n" +
		"  - files: \"**\"\n    except:\n      - github.com/acme/slow\n    option: optimize_for\n    value: SPEED\n  - files: \"**\"\n    module: github.com/acme/fast\n    option: optimize_for\n    value: CODE_SIZE\n" +
		"  - files: \"**\"\n    option: java_package\n    prefix: com.acme\n  - files: \"**\"\n    option: go_package\n    prefix: github.com/acme/gen\n" +
		"  - files: \"**\"\n    option: objc_class_prefix\n    value: ACM\n  - files: \"**\"\n    option: swift_prefix\n    value: SWF\n" +
		"  - files: \"**\"\n    module: example.com/acme/x\n    option: csharp_namespace\n    value: Acme.X\n" +
		"  - files: \"**\"\n    option: java_multiple_files\n    value: \"true\"\n  - files: acme/v1/a.proto/**\n    option: java_package\n    value: com.acme.a\n"
	if string(out) != wantFile {
		t.Fatalf("v1 file:\n%s", out)
	}

	// Managed mode disabled reads for nothing; enabled with nothing
	// explicit is buf's heuristic; no plugin mapped writes no file and
	// reads the managed mode for nothing.
	g := parseGen(t, "version: v2\nmanaged:\n  enabled: false\n  override:\n    - file_option: java_package\n      value: x\nplugins:\n  - local: gen\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	if err != nil || l.Gen == nil || len(l.Gen.Overrides) != 0 || !strings.Contains(factsOf(facts), "buf.gen.yaml managed.enabled false -> nothing: managed mode is disabled") {
		t.Fatalf("disabled: %v %+v\n%s", err, l.Gen, factsOf(facts))
	}
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: x\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	if got := factsOf(facts); err != nil || l.Gen != nil || strings.Contains(got, "overrides:") || !strings.Contains(got, "buf.gen.yaml plugins !! no plugin mapped: no generation file is written\nbuf.gen.yaml managed !! no generation file is written, no override with it") {
		t.Fatalf("nothing mapped: %v %+v\n%s", err, l.Gen, got)
	}
	// Enabled alone is buf's defaults, nine overrides over every file
	// in buf's order.
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\nplugins:\n  - local: gen\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	if err != nil || !strings.Contains(factsOf(facts), strings.Join([]string{
		"buf.gen.yaml managed.enabled true -> overrides: files ** option cc_enable_arenas value true (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option csharp_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_multiple_files value true (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_outer_classname (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option java_package prefix com (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option objc_class_prefix (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option php_metadata_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option php_namespace (buf's default)",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option ruby_package (buf's default)",
	}, "\n")) || !reflect.DeepEqual(l.Gen.Overrides, func() []genfile.Override {
		var ds []genfile.Override
		for _, d := range managedDefaults {
			ds = append(ds, defaultOverride(d))
		}
		return ds
	}()) {
		t.Fatalf("enabled alone: %v\n%s\n%+v", err, factsOf(facts), l.Gen)
	}
	// Prefix and suffix rules map as buf reads them: per file the
	// prefix and suffix the matching rules set in order, a value rule
	// clearing both, java_package starting from the prefix com; an
	// override a later one covers whole is dropped.
	g = parseGen(t, `version: v2
managed:
  enabled: true
  override:
    - file_option: java_package_suffix
      value: pb
      path: sub
    - file_option: java_package_prefix
      value: org
    - file_option: java_package
      value: com.fixed
      path: sub/deep
    - file_option: java_package_prefix
      value: q
      path: subway
    - file_option: java_package_suffix
      value: x
      path: sub
    - file_option: go_package_prefix
      value: ""
    - file_option: ruby_package_suffix
      value: A
      path: sub
    - file_option: ruby_package
      value: R
plugins:
  - local: gen
    out: gen
`)
	l = &Layout{}
	facts, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	// The defaults, those no rule replaces whole, lead the file.
	defaultsBut := func(replaced ...string) []genfile.Override {
		var ds []genfile.Override
		for _, d := range managedDefaults {
			o := defaultOverride(d)
			if slices.Contains(replaced, o.Option) {
				continue
			}
			ds = append(ds, o)
		}
		return ds
	}
	wantOverrides := append(defaultsBut("java_package", "ruby_package"),
		genfile.Override{Files: "**", Option: "java_package", Prefix: "org"},
		genfile.Override{Files: "subway/**", Option: "java_package", Prefix: "q"},
		genfile.Override{Files: "sub/**", Option: "java_package", Prefix: "org", Suffix: "x"},
		genfile.Override{Files: "sub/deep/**", Option: "java_package", Suffix: "x"},
		genfile.Override{Files: "**", Option: "ruby_package", Value: "R"},
	)
	if err != nil || l.Gen == nil || !reflect.DeepEqual(l.Gen.Overrides, wantOverrides) {
		t.Fatalf("derivations: %v\n%+v", err, l.Gen)
	}
	wantFacts := strings.Join([]string{
		"buf.gen.yaml managed.override[0] file_option=java_package_suffix value=pb path=sub -> overrides: files sub/** option java_package prefix com suffix pb (with buf.gen.yaml managed.enabled true's prefix)",
		"buf.gen.yaml managed.override[1] file_option=java_package_prefix value=org -> overrides: files ** option java_package prefix org (after buf.gen.yaml managed.enabled true, which set no suffix)",
		"buf.gen.yaml managed.override[1] file_option=java_package_prefix value=org -> overrides: files sub/** option java_package prefix org suffix pb (with buf.gen.yaml managed.override[0] file_option=java_package_suffix value=pb path=sub's suffix)",
		"buf.gen.yaml managed.override[1] file_option=java_package_prefix value=org -> replacing buf.gen.yaml managed.enabled true's override of java_package for files **, buf.gen.yaml managed.override[0] file_option=java_package_suffix value=pb path=sub's override of java_package for files sub/**",
		"buf.gen.yaml managed.override[2] file_option=java_package path=sub/deep -> overrides: files sub/deep/** option java_package value com.fixed",
		"buf.gen.yaml managed.override[3] file_option=java_package_prefix value=q path=subway -> overrides: files subway/** option java_package prefix q (after buf.gen.yaml managed.override[1] file_option=java_package_prefix value=org, which set no suffix)",
		"buf.gen.yaml managed.override[4] file_option=java_package_suffix value=x path=sub -> overrides: files sub/** option java_package prefix org suffix x (with buf.gen.yaml managed.override[1] file_option=java_package_prefix value=org's prefix)",
		"buf.gen.yaml managed.override[4] file_option=java_package_suffix value=x path=sub -> overrides: files sub/deep/** option java_package suffix x (after buf.gen.yaml managed.override[2] file_option=java_package path=sub/deep's value, clearing the prefix)",
		"buf.gen.yaml managed.override[4] file_option=java_package_suffix value=x path=sub -> replacing buf.gen.yaml managed.override[1] file_option=java_package_prefix value=org's override of java_package for files sub/**, buf.gen.yaml managed.override[2] file_option=java_package path=sub/deep's override of java_package for files sub/deep/**",
		"buf.gen.yaml managed.override[5] file_option=go_package_prefix value= !! an empty prefix, clearing what buf's earlier rules set for the files: pb clears no override",
		"buf.gen.yaml managed.override[6] file_option=ruby_package_suffix value=A path=sub -> overrides: files sub/** option ruby_package suffix A (after buf.gen.yaml managed.enabled true, which set no prefix)",
		"buf.gen.yaml managed.override[7] file_option=ruby_package -> overrides: files ** option ruby_package value R",
		"buf.gen.yaml managed.override[7] file_option=ruby_package -> replacing buf.gen.yaml managed.enabled true's override of ruby_package for files **, buf.gen.yaml managed.override[6] file_option=ruby_package_suffix value=A path=sub's override of ruby_package for files sub/**",
	}, "\n")
	if got := factsOf(facts); !strings.Contains(got, wantFacts) {
		t.Fatalf("derivations facts:\n%s\nwant:\n%s", got, wantFacts)
	}

	// A rule whose intersections land on one scope twice writes and
	// reports one override for it.
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package_prefix\n      value: A\n    - file_option: java_package_suffix\n      value: S\n      path: sub\n    - file_option: java_package_prefix\n      value: B\n      path: sub\nplugins:\n  - local: gen\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	wantOverrides = append(defaultsBut("java_package"),
		genfile.Override{Files: "**", Option: "java_package", Prefix: "A"},
		genfile.Override{Files: "sub/**", Option: "java_package", Prefix: "B", Suffix: "S"},
	)
	r2 := "buf.gen.yaml managed.override[2] file_option=java_package_prefix value=B path=sub -> "
	if got := factsOf(facts); err != nil || !reflect.DeepEqual(l.Gen.Overrides, wantOverrides) || strings.Count(got, r2) != 2 ||
		!strings.Contains(got, r2+"overrides: files sub/** option java_package prefix B suffix S (with buf.gen.yaml managed.override[1] file_option=java_package_suffix value=S path=sub's suffix)\n"+r2+"replacing buf.gen.yaml managed.override[1] file_option=java_package_suffix value=S path=sub's override of java_package for files sub/**") {
		t.Fatalf("one scope twice: %v %+v\n%s", err, l.Gen, got)
	}

	// v1's rules hold buf's order: the per-file map, by option key as
	// written and then by path, after the forms and booleans, whatever
	// the document says first.
	g = parseGen(t, "version: v1\nmanaged:\n  enabled: true\n  override:\n    JAVA_PACKAGE_PREFIX:\n      a/b.proto: org\n    JAVA_PACKAGE:\n      a/b.proto: com.x\n    JAVA_MULTIPLE_FILES:\n      a/b.proto: \"false\"\n  java_multiple_files: true\n  java_package_prefix: com.acme\nplugins:\n  - name: go\n    out: gen\n")
	l = &Layout{}
	_, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	wantOverrides = append(defaultsBut("java_package", "java_multiple_files"),
		genfile.Override{Files: "**", Option: "java_package", Prefix: "com.acme"},
		genfile.Override{Files: "**", Option: "java_multiple_files", Value: "true"},
		genfile.Override{Files: "a/b.proto/**", Option: "java_multiple_files", Value: "false"},
		genfile.Override{Files: "a/b.proto/**", Option: "java_package", Prefix: "org"},
	)
	if err != nil || !reflect.DeepEqual(l.Gen.Overrides, wantOverrides) {
		t.Fatalf("v1 order: %v %+v", err, l.Gen)
	}

	// With no plugin mapped, a disabled or empty managed mode is what
	// it would have been.
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: false\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	facts, err = Gen(context.Background(), g, nil, Replacements{}, &Layout{}, nil, nil)
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
	if _, err := Gen(context.Background(), g, nil, unusedRepl, l, nil, nil); err == nil || err.Error() != "--plugin buf.build/acme/u0, --plugin buf.build/acme/u1: no generation file names such a plugin" {
		t.Fatalf("unused replacements: %v", err)
	}
	var badRepl Replacements
	if err := badRepl.Replace("plugin", "buf.build/nobody/knows=not-a-reference"); err != nil {
		t.Fatal(err)
	}
	g = parseGen(t, "version: v2\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	if _, err := Gen(context.Background(), g, nil, badRepl, l, nil, nil); err == nil || !strings.Contains(err.Error(), "--plugin buf.build/nobody/knows=not-a-reference") {
		t.Fatalf("a replacement that is no reference: %v", err)
	}
	if name, ref := bsrSplit("bsr.example.com:8443/acme/plugin:v1"); name != "bsr.example.com:8443/acme/plugin" || ref != "v1" {
		t.Fatalf("bsrSplit: %q %q", name, ref)
	}
	if name, ref := bsrSplit("bsr.example.com:8443/acme/plugin"); name != "bsr.example.com:8443/acme/plugin" || ref != "" {
		t.Fatalf("bsrSplit without a ref: %q %q", name, ref)
	}
	if _, err := Gen(context.Background(), nil, nil, Replacements{}, nil, nil, nil); err == nil {
		t.Fatal("no file: no error")
	}
}

// The disables, applied after every rule: an option alone drops its
// default and rules; a prefix alone its axis, an override left with
// no axis dropped; a module alone leaves the module out of every
// override, one scoped to the module dropped; an option and a
// module the same for the option; a prefix and a module the same for
// the overrides deriving from the prefix, the module keeping the
// suffix where one is set; a path, a field option or a field is
// unmapped, as is a module no path stands for (REQ-migrate-gen).
func TestGenManagedDisables(t *testing.T) {
	l := &Layout{Names: map[string]string{"buf.build/acme/own": "example.com/acme/own"}, DepPaths: map[string]string{"buf.build/googleapis/googleapis": "github.com/googleapis/googleapis"}}
	g := parseGen(t, `version: v2
managed:
  enabled: true
  override:
    - file_option: java_package_prefix
      value: org
    - file_option: java_package_suffix
      value: pb
    - file_option: go_package_prefix
      value: example.com/gen
    - file_option: csharp_namespace
      value: Own
      module: buf.build/acme/own
    - file_option: optimize_for
      value: SPEED
      module: buf.build/googleapis/googleapis
    - file_option: java_multiple_files
      value: "false"
      module: buf.build/googleapis/googleapis
    - file_option: java_package
      value: com.fixed
      path: fixed
    - file_option: csharp_namespace_prefix
      value: Acme
      path: cs
  disable:
    - file_option: objc_class_prefix
    - file_option: ruby_package_suffix
    - file_option: php_metadata_namespace_suffix
    - file_option: csharp_namespace_prefix
    - module: buf.build/acme/own
    - file_option: optimize_for
      module: buf.build/googleapis/googleapis
    - file_option: java_package_prefix
      module: buf.build/googleapis/googleapis
    - file_option: go_package
      path: legacy
    - field_option: jstype
    - file_option: cc_enable_arenas
      module: buf.build/nobody/knows
plugins:
  - local: gen
    out: gen
`)
	facts, err := Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := factsOf(facts)
	for _, want := range []string{
		"buf.gen.yaml managed.disable[0] file_option=objc_class_prefix -> nothing sets objc_class_prefix beyond the rules left: buf.gen.yaml managed.enabled true for objc_class_prefix dropped for every file",
		// A default buf computes from the file carries no axis: an axis
		// disable touches it not.
		"buf.gen.yaml managed.disable[1] file_option=ruby_package_suffix -> nothing: no rule sets ruby_package_suffix there",
		"buf.gen.yaml managed.disable[2] file_option=php_metadata_namespace_suffix -> nothing: no rule sets php_metadata_namespace_suffix there",
		"buf.gen.yaml managed.disable[3] file_option=csharp_namespace_prefix -> nothing sets csharp_namespace_prefix beyond the rules left: buf.gen.yaml managed.override[7] file_option=csharp_namespace_prefix value=Acme path=cs for csharp_namespace dropped for every file",
		"buf.gen.yaml managed.disable[4] module=buf.build/acme/own -> the module example.com/acme/own sees every option without buf.gen.yaml managed.enabled true for cc_enable_arenas, ",
		"buf.gen.yaml managed.override[3] file_option=csharp_namespace module=buf.build/acme/own for csharp_namespace, ",
		"buf.gen.yaml managed.disable[5] file_option=optimize_for module=buf.build/googleapis/googleapis -> the module github.com/googleapis/googleapis sees optimize_for without buf.gen.yaml managed.override[4] file_option=optimize_for module=buf.build/googleapis/googleapis for optimize_for: its overrides of the option recomputed, every module's excepting it",
		"buf.gen.yaml managed.disable[6] file_option=java_package_prefix module=buf.build/googleapis/googleapis -> the module github.com/googleapis/googleapis sees java_package_prefix without buf.gen.yaml managed.enabled true for java_package, buf.gen.yaml managed.override[0] file_option=java_package_prefix value=org for java_package: its overrides of the option recomputed, every module's excepting it",
		"buf.gen.yaml managed.disable[7] file_option=go_package path=legacy !! disables over a path: pb's overrides name what they set, excluding no path",
		"buf.gen.yaml managed.disable[8] field_option=jstype !! a field option: pb's overrides are file options",
		"buf.gen.yaml managed.disable[9] file_option=cc_enable_arenas module=buf.build/nobody/knows !! names the module buf.build/nobody/knows, which the configuration declares nowhere: no module path stands for it",
		// The module's own run: the suffix rule with no prefix before
		// it, the path value after.
		"buf.gen.yaml managed.override[1] file_option=java_package_suffix value=pb -> overrides: files ** module github.com/googleapis/googleapis option java_package suffix pb (for the module github.com/googleapis/googleapis, under its disables)",
		"buf.gen.yaml managed.override[6] file_option=java_package path=fixed -> overrides: files fixed/** module github.com/googleapis/googleapis option java_package value com.fixed (for the module github.com/googleapis/googleapis, under its disables)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("facts lack %q:\n%s", want, got)
		}
	}
	// The csharp prefix rule, disabled for every file, is never run.
	if strings.Contains(got, "option csharp_namespace prefix Acme") {
		t.Errorf("a disabled rule ran:\n%s", got)
	}
	own, both := []string{"example.com/acme/own"}, []string{"example.com/acme/own", "github.com/googleapis/googleapis"}
	wantOverrides := []genfile.Override{
		{Files: "**", Option: "cc_enable_arenas", Value: "true", Except: own},
		{Files: "**", Option: "csharp_namespace", Bare: true, Except: own},
		{Files: "**", Option: "java_multiple_files", Value: "true", Except: own},
		{Files: "**", Option: "java_outer_classname", Bare: true, Except: own},
		{Files: "**", Option: "php_metadata_namespace", Bare: true, Except: own},
		{Files: "**", Option: "php_namespace", Bare: true, Except: own},
		{Files: "**", Option: "ruby_package", Bare: true, Except: own},
		// The suffix rule's state, prefix org and suffix pb, left out
		// of own whole and of googleapis, whose own run follows.
		{Files: "**", Option: "java_package", Prefix: "org", Suffix: "pb", Except: both},
		{Files: "**", Option: "go_package", Prefix: "example.com/gen", Except: own},
		// An override scoped to another module is no module disable's
		// business; a value override is no axis disable's.
		{Files: "**", Module: "github.com/googleapis/googleapis", Option: "java_multiple_files", Value: "false"},
		{Files: "fixed/**", Option: "java_package", Value: "com.fixed", Except: both},
		// googleapis under its disables: the suffix without the prefix
		// rules, the path value after it.
		{Files: "**", Module: "github.com/googleapis/googleapis", Option: "java_package", Suffix: "pb"},
		{Files: "fixed/**", Module: "github.com/googleapis/googleapis", Option: "java_package", Value: "com.fixed"},
	}
	if l.Gen == nil || !slices.EqualFunc(l.Gen.Overrides, wantOverrides, genfile.Override.Equal) {
		t.Errorf("overrides:\n%+v\nwant\n%+v", l.Gen.Overrides, wantOverrides)
	}
}

// defaultOverride is the override one of buf's defaults spells over
// every file, as the mapper emits it.
func defaultOverride(r managedRule) genfile.Override {
	o := genfile.Override{Files: "**", Option: r.option}
	switch r.kind {
	case ruleValue:
		o.Value = r.value
	case ruleBare:
		o.Bare = true
	case ruleDerive:
		option, isPrefix, _ := derivation(r.option)
		o.Option = option
		if isPrefix {
			o.Prefix = r.value
		} else {
			o.Suffix = r.value
		}
	}
	return o
}

// A local command pb's schema refuses — an unclean path, a
// backslash — is an unmapped fact naming it, never a file the verb
// refuses; the scalar and list forms alike.
func TestGenLocalCommandRefused(t *testing.T) {
	g := parseGen(t, "version: v2\nplugins:\n  - local: [tools//gen, x]\n    out: gen/a\n  - local: ./\n    out: gen/b\n  - local: [tools\\gen]\n    out: gen/c\n  - local: [ok, tools//x]\n    out: gen/d\n")
	l := &Layout{}
	facts, err := Gen(context.Background(), g, nil, Replacements{}, l, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := factsOf(facts)
	for _, want := range []string{
		"buf.gen.yaml plugins[0].local tools//gen x !! pb's schema refuses the command: local \"tools//gen\" is not a clean path",
		"buf.gen.yaml plugins[1].local ./ !! pb's schema refuses the command: local \"./\" is not a clean path",
		"buf.gen.yaml plugins[2].local tools\\gen !! pb's schema refuses the command: local \"tools\\\\gen\": paths are written with forward slashes",
		"buf.gen.yaml plugins[3].local ok tools//x -> local: ok tools//x",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("facts lack %q:\n%s", want, got)
		}
	}
	if l.Gen == nil || len(l.Gen.Plugins) != 1 || l.Gen.Plugins[0].Ref != "ok" {
		t.Errorf("file %+v", l.Gen)
	}
}

// fakeTags lists the tags given per repository, counting the
// listings; a repository given none fails the listing.
type fakeTags map[string][]string

func (f fakeTags) list(_ context.Context, repo string) ([]string, error) {
	tags, ok := f[repo]
	if !ok {
		return nil, errors.New("registry unreachable")
	}
	f[repo+" listed"] = append(f[repo+" listed"], "")
	return tags, nil
}

// A tree's paths, for the inputs' mapping: directories end in a
// slash.
func treeStat(entries ...string) StatFunc {
	return func(rel string) (bool, bool) {
		for _, e := range entries {
			if strings.TrimSuffix(e, "/") == rel {
				return true, strings.HasSuffix(e, "/")
			}
		}
		return false, false
	}
}

// v2 inputs map to the file's entries' files patterns: a directory
// path under a module its module-relative path and everything under
// it, a file path the file; a path under no module, a whole module
// among several, a missing path, a module-relative path another
// module holds too, an exclusion and an input of another kind are
// unmapped facts; a whole module alone, or a directory input without
// paths, restricts nothing; a path outside its input directory is
// buf's own refusal. A template's entries follow the file's with its
// own inputs; its managed mode maps where it gives the file's
// overrides, its clean where it agrees (REQ-migrate-gen).
func TestGenInputsAndTemplates(t *testing.T) {
	l := &Layout{Modules: map[string]*modfile.File{"proto": {}, "other": {}}}
	stat := treeStat("proto/acme/api/", "proto/acme/x.proto", "proto/acme/dup/", "other/acme/dup/", "elsewhere/")
	g := parseGen(t, `version: v2
clean: true
inputs:
  - directory: .
    paths: [proto/acme/api, proto/acme/x.proto, proto/acme/dup, elsewhere, proto, proto/acme/missing]
    exclude_paths: [proto/acme/api/old]
    types: [acme.api.Ping]
  - module: buf.build/acme/petapis
  - directory: proto
    paths: [proto/acme/api]
plugins:
  - local: gen
    out: gen
    include_imports: true
  - local: gen2
    out: gen2
managed:
  enabled: true
  override:
    - file_option: java_package
      value: com.acme
`)
	same := parseGen(t, "version: v2\nclean: true\ninputs:\n  - directory: .\n    paths: [proto/acme/x.proto]\nplugins:\n  - local: gen3\n    out: gen3\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: com.acme\n")
	differs := parseGen(t, "version: v2\nplugins:\n  - local: gen4\n    out: gen4\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: com.other\n    - file_option: go_package_prefix\n      value: example.com/t\n")
	empty := parseGen(t, "version: v2\nbogus: x\nplugins:\n  - local: gen5\n    out: gen5\n")
	facts, err := Gen(context.Background(), g, []Template{{"buf.gen.a.yaml", same}, {"buf.gen.b.yaml", differs}, {"buf.gen.c.yaml", empty}}, Replacements{}, l, nil, stat)
	if err != nil {
		t.Fatal(err)
	}
	got := factsOf(facts)
	for _, want := range []string{
		"buf.gen.yaml plugins[0].local gen -> local: gen\nbuf.gen.yaml plugins[0].out gen -> out: gen\nbuf.gen.yaml plugins[0].include_imports true -> include_imports: true\n",
		"buf.gen.yaml plugins[1].out gen2 -> out: gen2\nbuf.gen.yaml inputs[0].paths[0] proto/acme/api -> files: acme/api/**\n",
		"buf.gen.yaml inputs[0].paths[1] proto/acme/x.proto -> files: acme/x.proto\n",
		"buf.gen.yaml inputs[0].paths[2] proto/acme/dup !! the module-relative path acme/dup lies in other/acme/dup too: a pattern would name both\n",
		"buf.gen.yaml inputs[0].paths[3] elsewhere !! under no workspace module: pb generates over the modules' own files\n",
		"buf.gen.yaml inputs[0].paths[4] proto !! a whole module among several: pb's patterns are module-relative and name every module\n",
		"buf.gen.yaml inputs[0].paths[5] proto/acme/missing !! no such path\n",
		"buf.gen.yaml inputs[0].exclude_paths[0] proto/acme/api/old !! pb's patterns name what to generate for, excluding nothing\n",
		"buf.gen.yaml inputs[0].types !! pb generates over the workspace's own files under one strategy\n",
		"buf.gen.yaml inputs[1].module buf.build/acme/petapis !! an input pb reads nothing from: pb generates over the workspace's own files\n",
		"buf.gen.yaml inputs[2].paths[0] proto/acme/api -> files: acme/api/**\n",
		"buf.gen.yaml clean true -> clean: true on each of its entries: the files differ\n",
		"buf.gen.a.yaml plugins[0].local gen3 -> local: gen3\nbuf.gen.a.yaml plugins[0].out gen3 -> out: gen3\nbuf.gen.a.yaml inputs[0].paths[0] proto/acme/x.proto -> files: acme/x.proto\nbuf.gen.a.yaml clean true -> clean: true on each of its entries: the files differ\nbuf.gen.a.yaml managed.enabled true -> overrides: files ** option cc_enable_arenas value true (buf's default)\n",
		"buf.gen.a.yaml managed.override[0] file_option=java_package -> overrides: files ** option java_package value com.acme\nbuf.gen.a.yaml managed.override[0] file_option=java_package -> replacing buf.gen.a.yaml managed.enabled true's override of java_package for files **\nbuf.gen.a.yaml managed -> the overrides buf.gen.yaml's managed mode gives\n",
		"buf.gen.b.yaml plugins[0].out gen4 -> out: gen4\nbuf.gen.b.yaml clean false -> the file's entries clean nothing: the files differ\nbuf.gen.b.yaml managed.enabled true -> overrides: files ** option cc_enable_arenas value true (buf's default)\n",
		"buf.gen.b.yaml managed.override[0] file_option=java_package -> overrides: files ** option java_package value com.other\nbuf.gen.b.yaml managed.override[0] file_option=java_package -> replacing buf.gen.b.yaml managed.enabled true's override of java_package for files **\nbuf.gen.b.yaml managed.override[1] file_option=go_package_prefix value=example.com/t -> overrides: files ** option go_package prefix example.com/t\nbuf.gen.b.yaml managed !! differs from buf.gen.yaml's: pb's overrides are one set over every entry\n",
		"buf.gen.c.yaml plugins[0].out gen5 -> out: gen5\nbuf.gen.c.yaml clean false -> the file's entries clean nothing: the files differ\nbuf.gen.c.yaml managed !! none, while buf.gen.yaml's gives overrides: pb's overrides are one set over every entry\n",
		"buf.gen.yaml managed.enabled true -> overrides: files ** option ruby_package (buf's default)\nbuf.gen.yaml managed.override[0] file_option=java_package -> overrides: files ** option java_package value com.acme\nbuf.gen.yaml managed.override[0] file_option=java_package -> replacing buf.gen.yaml managed.enabled true's override of java_package for files **",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("facts lack %q:\n%s", want, got)
		}
	}
	f := l.Gen
	if f == nil || f.Clean || len(f.Plugins) != 5 || len(f.Overrides) != 9 || f.Overrides[8].Value != "com.acme" {
		t.Fatalf("file %+v", f)
	}
	pats := func(i int) string { return strings.Join(f.Plugins[i].Files, ",") }
	if pats(0) != "acme/api/**,acme/x.proto" || pats(1) != "acme/api/**,acme/x.proto" || !f.Plugins[0].IncludeImports || f.Plugins[1].IncludeImports {
		t.Errorf("the file's entries: %+v", f.Plugins[:2])
	}
	if pats(2) != "acme/x.proto" || pats(3) != "" || pats(4) != "" || f.Plugins[2].Ref != "gen3" || f.Plugins[4].Ref != "gen5" {
		t.Errorf("the templates' entries: %+v", f.Plugins[2:])
	}
	// The files' cleans differ: the cleaning files' entries carry
	// their own, the others none.
	for i, want := range []bool{true, true, true, false, false} {
		if f.Plugins[i].Clean != want {
			t.Errorf("plugins[%d].Clean = %v", i, f.Plugins[i].Clean)
		}
	}

	// One module: a path naming it whole restricts nothing, as an
	// input without paths does; a template alone is the first file,
	// its managed facts keyed by its name; a directory input naming
	// no paths is a path naming its directory.
	one := &Layout{Modules: map[string]*modfile.File{"proto": {}}}
	g = parseGen(t, "version: v2\ninputs:\n  - directory: .\n    paths: [proto]\n  - directory: .\n  - directory: proto/acme\n  - directory: proto\n  - directory: ../shared\n    paths: [../shared/x.proto]\nplugins:\n  - local: gen\n    out: gen\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: com.acme\n")
	facts, err = Gen(context.Background(), nil, []Template{{"buf.gen.only.yaml", g}}, Replacements{}, one, nil, treeStat("proto/", "proto/acme/"))
	if err != nil {
		t.Fatal(err)
	}
	got = factsOf(facts)
	for _, want := range []string{
		"buf.gen.only.yaml inputs[0].paths[0] proto -> every file of the module\nbuf.gen.only.yaml inputs[1].directory . -> every workspace file",
		"buf.gen.only.yaml inputs[2].directory proto/acme -> files: acme/**\nbuf.gen.only.yaml inputs[3].directory proto -> every file of the module\nbuf.gen.only.yaml inputs[4].directory ../shared !! outside the resolution root: pb generates over the workspace's own files\n",
		"buf.gen.only.yaml managed.override[0] file_option=java_package -> overrides: files ** option java_package value com.acme",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("facts lack %q:\n%s", want, got)
		}
	}
	if one.Gen == nil || one.Gen.Plugins[0].Files != nil || len(one.Gen.Overrides) != 9 {
		t.Errorf("one module: %+v", one.Gen)
	}
	// A lone template's disable facts are keyed by its name too.
	g = parseGen(t, "version: v2\nplugins:\n  - local: gen\n    out: gen\nmanaged:\n  enabled: true\n  disable:\n    - file_option: go_package\n")
	facts, err = Gen(context.Background(), nil, []Template{{"buf.gen.only.yaml", g}}, Replacements{}, one, nil, treeStat("proto/"))
	if err != nil || !strings.Contains(factsOf(facts), "buf.gen.only.yaml managed.disable[0] file_option=go_package -> nothing: no rule sets go_package there") {
		t.Errorf("a lone template's disable: %v\n%s", err, factsOf(facts))
	}

	// Every file with entries agreeing on clean: the file's clean, no
	// entry's own; a file with none to clean for is read for nothing.
	first := parseGen(t, "version: v2\nclean: true\nplugins:\n  - local: gen\n    out: gen\n")
	agree := parseGen(t, "version: v2\nclean: true\nplugins:\n  - local: gen6\n    out: gen6\n")
	noEntry := parseGen(t, "version: v2\nclean: true\nplugins: []\n")
	facts, err = Gen(context.Background(), first, []Template{{"buf.gen.d.yaml", agree}, {"buf.gen.e.yaml", noEntry}}, Replacements{}, l, nil, stat)
	if err != nil || !l.Gen.Clean || !strings.Contains(factsOf(facts), "buf.gen.yaml clean true -> clean: true\n") || !strings.Contains(factsOf(facts), "buf.gen.d.yaml clean true -> clean: true, buf.gen.yaml's, every file agreeing\n") || !strings.HasSuffix(factsOf(facts), "buf.gen.e.yaml clean true -> nothing: the file has no entry to clean for") {
		t.Errorf("files agreeing on clean: %v %+v\n%s", err, l.Gen, factsOf(facts))
	}
	for i, p := range l.Gen.Plugins {
		if p.Clean {
			t.Errorf("plugins[%d] carries its own clean under the file's", i)
		}
	}

	// A directory naming a module among several is a whole module.
	g = parseGen(t, "version: v2\ninputs:\n  - directory: proto\nplugins:\n  - local: gen\n    out: gen\n")
	facts, err = Gen(context.Background(), g, nil, Replacements{}, l, nil, stat)
	if err != nil || !strings.Contains(factsOf(facts), "buf.gen.yaml inputs[0].directory proto !! a whole module among several") || l.Gen.Plugins[0].Files != nil {
		t.Errorf("a module directory input: %v\n%s", err, factsOf(facts))
	}
	// The root module: a path's module-relative spelling is the path,
	// a dot-led one included.
	root := &Layout{Modules: map[string]*modfile.File{".": {}}}
	g = parseGen(t, "version: v2\ninputs:\n  - directory: .\n    paths: [.gen/x.proto, api]\nplugins:\n  - local: gen\n    out: gen\n")
	facts, err = Gen(context.Background(), g, nil, Replacements{}, root, nil, treeStat(".gen/x.proto", "api/"))
	if err != nil || strings.Join(root.Gen.Plugins[0].Files, ",") != ".gen/x.proto,api/**" {
		t.Errorf("root module: %v %+v\n%s", err, root.Gen, factsOf(facts))
	}

	// A path outside its input directory is buf's own refusal; so is
	// an escaping one.
	for _, text := range []string{
		"version: v2\ninputs:\n  - directory: proto\n    paths: [other/x]\nplugins:\n  - local: gen\n    out: gen\n",
		"version: v2\ninputs:\n  - directory: .\n    paths: [../x]\nplugins:\n  - local: gen\n    out: gen\n",
	} {
		if _, err := Gen(context.Background(), parseGen(t, text), nil, Replacements{}, l, nil, stat); err == nil || !errors.Is(err, bufconfig.ErrInvalid) {
			t.Errorf("outside the input: %v", err)
		}
	}
}

// A versionless plugin takes the highest version tag the registry
// lists, listed once per name however many entries name it; a
// listing that fails, or one holding no version tag, leaves the
// entry unmapped naming the flag's form; with no registry access the
// listing fails as such. A version named that no tag can spell —
// buf admits semver's build suffix — is unmapped, never a file pb
// refuses.
func TestGenVersionlessPlugin(t *testing.T) {
	g := parseGen(t, "version: v2\nplugins:\n  - remote: buf.build/grpc/go\n    out: gen/a\n  - remote: buf.build/grpc/go\n    out: gen/b\n  - remote: buf.build/bufbuild/es\n    out: gen/es\n  - remote: buf.build/grpc/web\n    out: gen/web\n  - remote: buf.build/protocolbuffers/go:v1.36.0+meta\n    out: gen/meta\n")
	tags := fakeTags{
		CatalogRepository("buf.build/grpc/go"):     {"v1.5.1", "sha256-ab.sig", "v1.6.2", "v1.6.10"},
		CatalogRepository("buf.build/bufbuild/es"): {"latest"},
	}
	l := &Layout{}
	facts, err := Gen(context.Background(), g, nil, Replacements{}, l, tags.list, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := factsOf(facts)
	for _, want := range []string{
		"buf.gen.yaml plugins[0].remote buf.build/grpc/go -> ref: ghcr.io/greatliontech/pb-plugins/grpc/go:v1.6.10 (the catalog; no version named, the highest tag published)",
		"buf.gen.yaml plugins[1].remote buf.build/grpc/go -> ref: ghcr.io/greatliontech/pb-plugins/grpc/go:v1.6.10 (the catalog; no version named, the highest tag published)",
		"buf.gen.yaml plugins[2].remote buf.build/bufbuild/es !! the catalog publishes no version of it yet: pass --plugin buf.build/bufbuild/es=<reference>",
		"buf.gen.yaml plugins[3].remote buf.build/grpc/web !! listing the catalog's tags failed: registry unreachable: name a version or pass --plugin buf.build/grpc/web=<reference>",
		"buf.gen.yaml plugins[4].remote buf.build/protocolbuffers/go:v1.36.0+meta !! no tag spells the version: ",
		"contains '+': pass --plugin buf.build/protocolbuffers/go=<reference>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("facts lack %q:\n%s", want, got)
		}
	}
	if n := len(tags[CatalogRepository("buf.build/grpc/go")+" listed"]); n != 1 {
		t.Errorf("grpc/go listed %d times", n)
	}
	if len(l.Gen.Plugins) != 2 || l.Gen.Plugins[1].Ref != "ghcr.io/greatliontech/pb-plugins/grpc/go:v1.6.10" {
		t.Errorf("file %+v", l.Gen.Plugins)
	}
	facts, err = Gen(context.Background(), g, nil, Replacements{}, &Layout{}, nil, nil)
	if err != nil || !strings.Contains(factsOf(facts), "buf.gen.yaml plugins[0].remote buf.build/grpc/go !! listing the catalog's tags failed: no registry access: name a version or pass --plugin buf.build/grpc/go=<reference>") {
		t.Errorf("no lister: %v\n%s", err, factsOf(facts))
	}
}
