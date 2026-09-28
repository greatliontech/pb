package dep

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

// A fork stands in for a dependency's dependency: the workspace
// replaces example.com/x with example.com/y at v1.0.0, and every reader
// of the build reads x through y — its requirements, its files, its
// pin — while x itself, served nowhere, is never fetched
// (workspace.md REQ-work-replace, dep-verbs.md REQ-dep-tidy,
// REQ-dep-download).
func TestReplaceStandsTheForkForTheOriginal(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/a: v1.0.0\n  example.com/b: v1.0.0\n"),
		"m/m.proto": "syntax = \"proto3\";\nimport \"a.proto\";\nimport \"b.proto\";\n",
	})
	fx.serve(t, "example.com/a", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/a", "  example.com/x: v1.0.0\n"),
		"a.proto": "syntax = \"proto3\";\nimport \"x.proto\";\n",
	})
	// A second requirer at a higher version: selection runs over x's
	// versions, and every one of them reads through the fork.
	fx.serve(t, "example.com/b", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/b", "  example.com/x: v1.1.0\n"),
		"b.proto": "syntax = \"proto3\";\n",
	})
	// The fork: the original's file under the original's import path,
	// and a requirement of its own the original never declared.
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/y", "  example.com/z: v1.0.0\n"),
		"x.proto": "syntax = \"proto3\";\n// the fork\n",
	})
	fx.serve(t, "example.com/z", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/z", ""),
		"z.proto": "syntax = \"proto3\";\n",
	})
	for _, served := range []string{"example.com/a", "example.com/b", "example.com/y", "example.com/z"} {
		fx.Endpoint(served, "v1.0.0", "info", `{"version":"v1.0.0"}`)
	}
	s := fx.session(t, ".")
	list, mods, err := s.Modules(ctx)
	if err != nil {
		t.Fatalf("Modules: %v", err)
	}
	selected := map[string]string{}
	for _, r := range list {
		selected[r.Path] = r.Version.String()
	}
	if selected["example.com/x"] != "v1.1.0" || selected["example.com/z"] != "v1.0.0" || selected["example.com/a"] != "v1.0.0" {
		t.Fatalf("build list = %v, want a, x at the highest required version (read through the fork) and the fork's own requirement z", selected)
	}
	if _, fork := selected["example.com/y"]; fork {
		t.Fatalf("build list = %v: the fork joined the build in its own right", selected)
	}
	var forked bool
	for _, m := range mods {
		if m.Path == "example.com/x" {
			forked = strings.Contains(string(m.Files["x.proto"]), "the fork")
		}
	}
	if !forked {
		t.Fatal("x's file set is not the fork's")
	}
	if _, ok := s.Lock.Module("example.com/y", "v1.0.0"); !ok {
		t.Fatal("the fork is not pinned under its own path")
	}
	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		if _, ok := s.Lock.Module("example.com/x", v); ok {
			t.Fatalf("the replaced path was pinned at %s: it was fetched", v)
		}
	}
	if n := len(s.Lock.Modules); n != 4 {
		t.Fatalf("%d pins, want a, b, y and z: the fork pinned once for both versions of x", n)
	}

	var out bytes.Buffer
	if err := Download(ctx, s, &out); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/x@v1.1.0 => example.com/y@v1.0.0\n") {
		t.Fatalf("download lines = %q, want the replaced module named with its replacement", out.String())
	}
	out.Reset()
	if err := Graph(ctx, s, &out); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if g := out.String(); !strings.Contains(g, "example.com/x@v1.0.0 example.com/z@v1.0.0\n") || !strings.Contains(g, "example.com/x@v1.1.0 example.com/z@v1.0.0\n") {
		t.Fatalf("graph = %q, want every version of x's edges read from the fork's module file", g)
	}

	before := fx.read(t, "m/pb.yaml")
	s2 := fx.session(t, ".")
	s2.Client.Cache = s.Client.Cache
	if err := Tidy(ctx, s2, io.Discard); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if got := fx.read(t, "m/pb.yaml"); got != before {
		t.Fatalf("tidy rewrote m/pb.yaml to %q: the declaration stays on the replaced path's consumer", got)
	}
	if _, ok := s2.Lock.Module("example.com/y", "v1.0.0"); !ok {
		t.Fatal("tidy pruned the fork's pin: it is inside the graph read through the replacement")
	}
}

// Update never consults a replaced path's origin (REQ-dep-update): the
// sweep leaves the declaration and says so while moving the rest, and
// naming the path fails.
func TestUpdateLeavesAReplacedPath(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/a: v1.0.0\n  example.com/x: v1.0.0\n"),
	})
	for _, v := range []string{"v1.0.0", "v1.2.0"} {
		fx.serve(t, "example.com/a", v, map[string]string{"pb.yaml": ws("example.com/a", "")})
		fx.Endpoint("example.com/a", v, "info", `{"version":"`+v+`"}`)
	}
	fx.Endpoints[fetchtest.ProxyHost+"/example.com/a/@v/list"] = []byte("v1.0.0\nv1.2.0\n")
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/y", "")})
	fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)

	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Update(ctx, s, &out, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, want := fx.read(t, "m/pb.yaml"), ws("example.com/m", "  example.com/a: v1.2.0\n  example.com/x: v1.0.0\n"); got != want {
		t.Fatalf("m/pb.yaml = %q, want a moved and the replaced x left", got)
	}
	if !strings.Contains(out.String(), "example.com/x: replaced by example.com/y@v1.0.0, declaration left\n") {
		t.Fatalf("update lines = %q, want the replaced path reported", out.String())
	}
	if _, ok := s.Lock.Module("example.com/x", "v1.0.0"); ok {
		t.Fatal("the replaced path was pinned: update fetched it")
	}

	err := Update(ctx, fx.session(t, "."), io.Discard, nil, "example.com/x")
	if err == nil || !strings.Contains(err.Error(), "replaced by example.com/y@v1.0.0") {
		t.Fatalf("named update of a replaced path: err = %v, want the refusal naming the replacement", err)
	}
	// Placed before any argument moves: a plugin named beside the
	// replaced path stays as pinned.
	fx.write(t, "pb.gen.yaml", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	s = fx.session(t, ".")
	up := &stubUpdater{lock: s.Lock, after: lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("22", 32)}}
	err = Update(ctx, s, io.Discard, up, "ghcr.io/o/p:v1", "example.com/x")
	if err == nil || !strings.Contains(err.Error(), "replaced by example.com/y@v1.0.0") || up.got != "" {
		t.Fatalf("a replaced path beside a plugin: err = %v, updater saw %q; want the run failed whole with the plugin untouched", err, up.got)
	}
}

// A synthesized replacement declares nothing: the replaced path's
// nodes carry no edges and its files are the archive's
// (REQ-work-replace, module-resolution.md REQ-resolve-synthesis).
func TestReplaceBySynthesizedModule(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/x: v1.0.0\n"),
	})
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{"x.proto": "syntax = \"proto3\";\n"})
	fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	s := fx.session(t, ".")
	list, mods, err := s.Modules(ctx)
	if err != nil {
		t.Fatalf("Modules: %v", err)
	}
	var file string
	for _, m := range mods {
		if m.Path == "example.com/x" {
			file = string(m.Files["x.proto"])
		}
	}
	if len(list) != 1 || list[0].Path != "example.com/x" || file == "" {
		t.Fatalf("build list = %v, x's file = %q: want x alone, carrying the synthesized fork's file", list, file)
	}
	var out bytes.Buffer
	if err := Graph(ctx, s, &out); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if strings.Contains(out.String(), "example.com/x@v1.0.0 ") {
		t.Fatalf("graph = %q, want no edge from x: the synthesized fork declares none", out.String())
	}
}

// A module required both as a replacement and in its own right is two
// modules of the build sharing files — the state the compile refuses
// as a duplicate provider (REQ-work-replace, generation.md
// REQ-gen-compile).
func TestReplacementRequiredInItsOwnRightIsAnotherModule(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/x: v1.0.0\n  example.com/y: v1.0.0\n"),
	})
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/y", ""),
		"x.proto": "syntax = \"proto3\";\n",
	})
	fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	s := fx.session(t, ".")
	_, mods, err := s.Modules(ctx)
	if err != nil {
		t.Fatalf("Modules: %v", err)
	}
	providers := map[string]int{}
	for _, m := range mods {
		if _, ok := m.Files["x.proto"]; ok {
			providers[m.Path]++
		}
	}
	if providers["example.com/x"] != 1 || providers["example.com/y"] != 1 || len(mods) != 3 {
		t.Fatalf("providers of x.proto = %v over %d modules, want x and y each beside the workspace module", providers, len(mods))
	}
	var amb *compile.AmbiguousError
	if _, err := compile.Compile(ctx, mods); !errors.As(err, &amb) || len(amb.Paths) != 1 || amb.Paths[0].Path != "x.proto" {
		t.Fatalf("Compile = %v, want x.proto refused as provided by both x and y", err)
	}
}

// The replacement's provenance is judged under its own path
// (REQ-work-replace): a rule requiring provenance of the replaced path
// never fires, and one on the replacement's path refuses the fetch.
func TestReplacementJudgedUnderItsOwnPath(t *testing.T) {
	for name, rule := range map[string]struct {
		prefix string
		ok     bool
	}{
		"rule on the replaced path": {"example.com/x", true},
		"rule on the replacement":   {"example.com/y", false},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newDep(t, map[string]string{
				"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
				"m/pb.yaml": ws("example.com/m", "  example.com/x: v1.0.0\n"),
			})
			fx.serve(t, "example.com/y", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/y", "")})
			fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)
			s := fx.session(t, ".")
			s.Client.Policy = &trust.Policy{Modules: []trust.Rule{{Prefix: rule.prefix, Require: trust.RequireProvenance}}}
			_, _, err := s.Modules(ctx)
			if rule.ok != (err == nil) || (err != nil && !strings.Contains(err.Error(), "example.com/y@v1.0.0 requires provenance")) {
				t.Fatalf("Modules under a provenance rule on %s: err = %v, want refused only under the replacement's own path", rule.prefix, err)
			}
		})
	}
}
