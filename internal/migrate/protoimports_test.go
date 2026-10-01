package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/migrate/bufconfig"
)

// A module whose file imports a sibling workspace module's file
// declares the sibling at the version discovery names for its path,
// or a replacement's; one importing a bundled import declares the
// table's provider the same way; a well-known import, the module's
// own file, a declared dependency's and an import no module provides
// declare nothing; a file whose imports cannot be read is an unmapped
// fact; one fact per module and provider, the first importing file
// named; the configuration's directory below the root read through
// it (REQ-migrate-imports).
func TestImports(t *testing.T) {
	ws := tree(t, map[string]string{
		"repo/cfg/buf.yaml": "version: v2\nmodules:\n  - path: a\n    excludes: [a/vendor]\n  - path: b\n  - path: c\n    includes: [c/c, c/shared]\n",
		// Outside buf's reading of its module — excluded, or outside
		// the includes — a file is read for nothing and provides nothing.
		"repo/cfg/a/vendor/junk.proto": "syntax = \"proto3\";\nimport \"google/protobuf/go_features.proto\"\n",
		"repo/cfg/a/vendor/v.proto":    "syntax = \"proto3\";\n",
		"repo/cfg/c/other/o.proto":     "syntax = \"proto3\";\nimport \"google/protobuf/go_features.proto\";\n",
		"repo/cfg/a/acme/a.proto":      "syntax = \"proto3\";\npackage acme;\nimport \"google/protobuf/empty.proto\";\nimport \"acme/other.proto\";\n",
		"repo/cfg/a/acme/other.proto":  "syntax = \"proto3\";\npackage acme;\n",
		"repo/cfg/b/b/first.proto":     "edition = \"2023\";\npackage b;\nimport \"acme/a.proto\";\nimport \"google/protobuf/go_features.proto\";\nimport \"google/api/annotations.proto\";\nimport \"nowhere/x.proto\";\n",
		"repo/cfg/b/b/second.proto":    "syntax = \"proto3\";\npackage b;\nimport \"acme/other.proto\";\nimport \"c/c.proto\";\nimport \"shared/dup.proto\";\nimport \"vendor/v.proto\";\n",
		"repo/cfg/a/shared/dup.proto":  "syntax = \"proto3\";\n",
		"repo/cfg/c/shared/dup.proto":  "syntax = \"proto3\";\n",
		"repo/cfg/b/b/broken.proto":    "syntax = \"proto3\";\nimport \"acme/a.proto\"\n",
		"repo/cfg/c/c/c.proto":         "syntax = \"proto3\";\npackage c;\nimport \"acme/a.proto\";\n",
		// A sibling's copy of a well-known import is no provider of
		// it; a file that is no proto is read for nothing.
		"repo/cfg/c/google/protobuf/empty.proto": "syntax = \"proto3\";\n",
		"repo/cfg/b/README.md":                   "not a proto\n",
	})
	cfg, err := bufconfig.ParseFile([]byte(readTree(t, ws, "repo/cfg/buf.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	src := &Source{File: cfg, Rel: "cfg"}
	l, err := Modules(src, "example.com/acme/mono")
	if err != nil {
		t.Fatal(err)
	}
	var repl Replacements
	if err := repl.Replace("dep", "github.com/protocolbuffers/protobuf-go/src=github.com/protocolbuffers/protobuf-go/src@v1.36.12"); err != nil {
		t.Fatal(err)
	}
	d := &fakeDiscovery{latest: map[string]string{"example.com/acme/mono/a": "v0.0.0-20240102030405-abcdefabcdef", "example.com/acme/mono/c": "v0.3.0"}}
	facts, err := Imports(context.Background(), ws, "repo/cfg", src, d, repl, l)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"b/b/broken.proto !! its imports unread: parsing b/b/broken.proto: b/b/broken.proto:3:1: syntax error: expecting ';'",
		"b/b/first.proto import acme/a.proto -> deps: example.com/acme/mono/a@v0.0.0-20240102030405-abcdefabcdef (a workspace module: buf's modules import one another freely, pb declares the version consumers require, discovered)",
		"b/b/first.proto import google/protobuf/go_features.proto -> deps: github.com/protocolbuffers/protobuf-go/src@v1.36.12 (buf bundles the import, pb's toolchain ships it not, --dep)",
		"b/b/second.proto import c/c.proto -> deps: example.com/acme/mono/c@v0.3.0 (a workspace module: buf's modules import one another freely, pb declares the version consumers require, discovered)",
		"b/b/second.proto import shared/dup.proto !! provided by example.com/acme/mono/a and example.com/acme/mono/c: pb never picks a provider, declare the one meant",
		// The path declared once: c reuses the version b's declaration
		// made.
		"c/c/c.proto import acme/a.proto -> deps: example.com/acme/mono/a@v0.0.0-20240102030405-abcdefabcdef (a workspace module: buf's modules import one another freely, pb declares the version consumers require, declared already)",
	}, "\n")
	if got := factsOf(facts); got != want {
		t.Fatalf("facts:\n%s", got)
	}
	if deps := l.Modules["cfg/b"].Deps; len(deps) != 3 || deps["example.com/acme/mono/a"] != "v0.0.0-20240102030405-abcdefabcdef" || deps["example.com/acme/mono/c"] != "v0.3.0" || deps["github.com/protocolbuffers/protobuf-go/src"] != "v1.36.12" {
		t.Errorf("b declares %v", deps)
	}
	if deps := l.Modules["cfg/a"].Deps; len(deps) != 0 {
		t.Errorf("a declares %v", deps)
	}
	if deps := l.Modules["cfg/c"].Deps; len(deps) != 1 || deps["example.com/acme/mono/a"] == "" {
		t.Errorf("c declares %v", deps)
	}
	// A v1 workspace member's excludes are its own file's, relative to
	// the member's directory.
	wsV1 := tree(t, map[string]string{
		"repo/buf.work.yaml":       "version: v1\ndirectories:\n  - m\n  - n\n",
		"repo/m/buf.yaml":          "version: v1\nbuild:\n  excludes:\n    - vendor\n",
		"repo/m/vendor/junk.proto": "syntax = \"proto3\";\nimport \"google/protobuf/go_features.proto\"\n",
		"repo/m/m.proto":           "syntax = \"proto3\";\nimport \"n.proto\";\n",
		"repo/n/n.proto":           "syntax = \"proto3\";\n",
	})
	srcV1, _, _, err := ReadSource(wsV1, "repo")
	if err != nil {
		t.Fatal(err)
	}
	lV1, _ := Modules(srcV1, "example.com/acme/work")
	facts, err = Imports(context.Background(), wsV1, "repo", srcV1, &fakeDiscovery{latest: map[string]string{"example.com/acme/work/n": "v0.1.0"}}, Replacements{}, lV1)
	if err != nil || factsOf(facts) != "m/m.proto import n.proto -> deps: example.com/acme/work/n@v0.1.0 (a workspace module: buf's modules import one another freely, pb declares the version consumers require, discovered)" {
		t.Fatalf("a v1 member's excludes: %v\n%s", err, factsOf(facts))
	}
	// A replacement's version is the replacement's, whatever the
	// layout declares.
	l5, _ := Modules(src, "example.com/acme/mono")
	l5.Versions = map[string]string{"github.com/protocolbuffers/protobuf-go/src": "v1.36.12"}
	facts, err = Imports(context.Background(), ws, "repo/cfg", src, &fakeDiscovery{latest: d.latest}, repl, l5)
	if err != nil || !strings.Contains(factsOf(facts), "protobuf-go/src@v1.36.12 (buf bundles the import, pb's toolchain ships it not, --dep)") {
		t.Fatalf("a replacement's version: %v\n%s", err, factsOf(facts))
	}
	// A replacement keyed by a path no file imports names what the
	// configuration never declares.
	var unused Replacements
	if err := unused.Replace("dep", "example.com/acme/mono/b=example.com/acme/mono/b@v1.0.0"); err != nil {
		t.Fatal(err)
	}
	l3, _ := Modules(src, "example.com/acme/mono")
	if _, err := Imports(context.Background(), ws, "repo/cfg", src, &fakeDiscovery{latest: d.latest}, unused, l3); err == nil || err.Error() != "--dep example.com/acme/mono/b: no file of the configuration imports what it provides" {
		t.Fatalf("an unused path-keyed replacement: %v", err)
	}
	// A version Deps declared for the path is the one Imports keeps.
	l4, _ := Modules(src, "example.com/acme/mono")
	l4.Versions = map[string]string{"example.com/acme/mono/a": "v5.0.0"}
	facts, err = Imports(context.Background(), ws, "repo/cfg", src, &fakeDiscovery{latest: d.latest}, Replacements{}, l4)
	if err != nil || l4.Modules["cfg/b"].Deps["example.com/acme/mono/a"] != "v5.0.0" || !strings.Contains(factsOf(facts), "example.com/acme/mono/a@v5.0.0 (a workspace module: buf's modules import one another freely, pb declares the version consumers require, declared already)") {
		t.Fatalf("a version declared already: %v %v\n%s", err, l4.Modules["cfg/b"].Deps, factsOf(facts))
	}
	// Discovered once per provider.
	if strings.Join(d.asked, ",") != "example.com/acme/mono/a,example.com/acme/mono/c" {
		t.Errorf("asked %v", d.asked)
	}
	// A provider whose version is not discovered is unmapped, naming
	// the replacement's form.
	l2, _ := Modules(src, "example.com/acme/mono")
	facts, err = Imports(context.Background(), ws, "repo/cfg", src, &fakeDiscovery{}, Replacements{}, l2)
	if err != nil || !strings.Contains(factsOf(facts), "b/b/first.proto import acme/a.proto !! no version discovered for example.com/acme/mono/a (no origin answers for example.com/acme/mono/a): pass --dep example.com/acme/mono/a=example.com/acme/mono/a@<version>") || len(l2.Modules["cfg/b"].Deps) != 0 {
		t.Fatalf("undiscovered: %v\n%s\n%v", err, factsOf(facts), l2.Modules["cfg/b"].Deps)
	}
}
