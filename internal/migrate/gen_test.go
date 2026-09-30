package migrate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
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
// each name buf's `buf.build/<owner>/<plugin>` whose repository under
// the rename rule is one a plugin reference's grammar accepts.
func TestPluginCatalogMatchesSpec(t *testing.T) {
	names := specNames(t, "../../docs/specs/migrate.md", "Generation")
	if strings.Join(names, " ") != strings.Join(Catalog, " ") || len(names) == 0 {
		t.Fatalf("the spec lists %v, the code %v", names, Catalog)
	}
	if !sort.StringsAreSorted(Catalog) {
		t.Errorf("the catalog is not sorted: %v", Catalog)
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
// held to; it moves with an entry's addition.
const catalogCommit = "8de44fb2bc92e130e6a8e5df999e54ed5361e2b5"

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
// GITHUB_TOKEN reading the private repository, the registry's
// credentials in the ambient store, and PBTRUSTEDROOT naming the
// trusted root the signatures are verified against.
func TestPluginCatalog(t *testing.T) {
	if os.Getenv("PB_LIVE_IMAGES") == "" {
		t.Skip("PB_LIVE_IMAGES unset: the registry is not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	tags := fakeTags{CatalogRepository("buf.build/grpc/go"): {"latest", "v1.5.1", "v1.6.2", "sha256-ab.sig"}}
	facts, err := Gen(context.Background(), v2, repl, l, tags.list)
	if err != nil {
		t.Fatal(err)
	}
	pbgo := "ghcr.io/greatliontech/pb-plugins/protocolbuffers/go"
	heuristic := " !! buf's own heuristic, a value computed per file from its package: pb declares values alone"
	want := strings.Join([]string{
		"buf.gen.yaml plugins[0].remote buf.build/protocolbuffers/go:v1.35.2 -> ref: " + pbgo + ":v1.35.2 (the catalog)",
		"buf.gen.yaml plugins[0].out gen/go -> out: gen/go",
		"buf.gen.yaml plugins[0].opt paths=source_relative -> opt: paths=source_relative",
		"buf.gen.yaml plugins[0].include_imports !! pb generates over the workspace's own files under one strategy",
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
		"  - ref: ghcr.io/greatliontech/pb-plugins/grpc/go:v1.6.2\n    out: gen/go\n" +
		"  - ref: " + pbgo + ":v1.36.0\n    out: gen/go36\n" +
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
	facts, err = Gen(context.Background(), v1, Replacements{}, l, nil)
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
	facts, err = Gen(context.Background(), g, Replacements{}, l, nil)
	if err != nil || l.Gen == nil || len(l.Gen.Overrides) != 0 || !strings.Contains(factsOf(facts), "buf.gen.yaml managed.enabled false -> nothing: managed mode is disabled") {
		t.Fatalf("disabled: %v %+v\n%s", err, l.Gen, factsOf(facts))
	}
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: x\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	l = &Layout{}
	facts, err = Gen(context.Background(), g, Replacements{}, l, nil)
	if got := factsOf(facts); err != nil || l.Gen != nil || strings.Contains(got, "overrides:") || !strings.Contains(got, "buf.gen.yaml plugins !! no plugin mapped: no generation file is written\nbuf.gen.yaml managed !! no generation file is written, no override with it") {
		t.Fatalf("nothing mapped: %v %+v\n%s", err, l.Gen, got)
	}
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: true\nplugins:\n  - local: gen\n    out: gen\n")
	facts, err = Gen(context.Background(), g, Replacements{}, &Layout{}, nil)
	if err != nil || !strings.Contains(factsOf(facts), "buf.gen.yaml managed.enabled true !! enabled with no explicit override: buf's own heuristic") {
		t.Fatalf("enabled alone: %v %s", err, factsOf(facts))
	}
	// With no plugin mapped, a disabled or empty managed mode is what
	// it would have been.
	g = parseGen(t, "version: v2\nmanaged:\n  enabled: false\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	facts, err = Gen(context.Background(), g, Replacements{}, &Layout{}, nil)
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
	if _, err := Gen(context.Background(), g, unusedRepl, l, nil); err == nil || err.Error() != "--plugin buf.build/acme/u0, --plugin buf.build/acme/u1: buf.gen.yaml names no such plugin" {
		t.Fatalf("unused replacements: %v", err)
	}
	var badRepl Replacements
	if err := badRepl.Replace("plugin", "buf.build/nobody/knows=not-a-reference"); err != nil {
		t.Fatal(err)
	}
	g = parseGen(t, "version: v2\nplugins:\n  - remote: buf.build/nobody/knows\n    out: gen\n")
	if _, err := Gen(context.Background(), g, badRepl, l, nil); err == nil || !strings.Contains(err.Error(), "--plugin buf.build/nobody/knows=not-a-reference") {
		t.Fatalf("a replacement that is no reference: %v", err)
	}
	if name, ref := bsrSplit("bsr.example.com:8443/acme/plugin:v1"); name != "bsr.example.com:8443/acme/plugin" || ref != "v1" {
		t.Fatalf("bsrSplit: %q %q", name, ref)
	}
	if name, ref := bsrSplit("bsr.example.com:8443/acme/plugin"); name != "bsr.example.com:8443/acme/plugin" || ref != "" {
		t.Fatalf("bsrSplit without a ref: %q %q", name, ref)
	}
	if _, err := Gen(context.Background(), nil, Replacements{}, nil, nil); err == nil {
		t.Fatal("no file: no error")
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
	facts, err := Gen(context.Background(), g, Replacements{}, l, tags.list)
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
	facts, err = Gen(context.Background(), g, Replacements{}, &Layout{}, nil)
	if err != nil || !strings.Contains(factsOf(facts), "buf.gen.yaml plugins[0].remote buf.build/grpc/go !! listing the catalog's tags failed: no registry access: name a version or pass --plugin buf.build/grpc/go=<reference>") {
		t.Errorf("no lister: %v\n%s", err, factsOf(facts))
	}
}
