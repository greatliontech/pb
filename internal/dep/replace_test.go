package dep

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
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
	out.Reset()
	if err := Why(ctx, s, &out, "example.com/x", "example.com/y", "example.com/z"); err != nil {
		t.Fatalf("Why: %v", err)
	}
	// The chain runs through the requirement graph's nodes — a's
	// requirement on x@v1.0.0 is the lexically least chain of its
	// length — and ends in the replacement step; the fork is needed
	// through x.
	wantWhy := "# example.com/x\nexample.com/m\nexample.com/a@v1.0.0\nexample.com/x@v1.0.0\nexample.com/x@v1.0.0 => example.com/y@v1.0.0\n\n" +
		"# example.com/y\nexample.com/m\nexample.com/a@v1.0.0\nexample.com/x@v1.0.0\nexample.com/x@v1.0.0 => example.com/y@v1.0.0\n\n" +
		"# example.com/z\nexample.com/m\nexample.com/a@v1.0.0\nexample.com/x@v1.0.0\nexample.com/z@v1.0.0\n"
	if out.String() != wantWhy {
		t.Fatalf("why = %q, want %q", out.String(), wantWhy)
	}

	before := fx.read(t, "m/pb.yaml")
	s2 := fx.session(t, ".")
	s2.Client.Cache = s.Client.Cache
	// A pin under x from before the replacement is outside the graph.
	s2.Lock.Modules = append(s2.Lock.Modules, lockfile.ModulePin{Path: "example.com/x", Version: "v1.1.0", Digest: "pb1:" + strings.Repeat("00", 32)})
	if err := Tidy(ctx, s2, io.Discard); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if got := fx.read(t, "m/pb.yaml"); got != before {
		t.Fatalf("tidy rewrote m/pb.yaml to %q: the declaration stays on the replaced path's consumer", got)
	}
	if _, ok := s2.Lock.Module("example.com/y", "v1.0.0"); !ok {
		t.Fatal("tidy pruned the fork's pin: it is inside the graph read through the replacement")
	}
	if _, ok := s2.Lock.Module("example.com/x", "v1.1.0"); ok {
		t.Fatal("tidy kept a pin under the replaced path, which is never fetched")
	}
	// The export report names the replaced module once, as every
	// report does (REQ-export-report, REQ-work-replace).
	out.Reset()
	if err := Export(ctx, s2, "out", "out", ExportOptions{}, &out); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/x@v1.1.0 => example.com/y@v1.0.0: 1 file(s)\n") {
		t.Fatalf("export report = %q, want the replaced module rendered with its replacement", out.String())
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
	// why names y's own chain first, then what it stands for.
	var out bytes.Buffer
	if err := Why(ctx, s, &out, "example.com/y"); err != nil {
		t.Fatalf("Why: %v", err)
	}
	if want := "# example.com/y\nexample.com/m\nexample.com/y@v1.0.0\nexample.com/m\nexample.com/x@v1.0.0\nexample.com/x@v1.0.0 => example.com/y@v1.0.0\n"; out.String() != want {
		t.Fatalf("why y = %q, want %q", out.String(), want)
	}
}

// One pinned replacement standing for two paths answers through each
// in raw-byte order (REQ-dep-why).
func TestWhyThroughEveryReplacedPath(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x2: example.com/y@v1.0.0\n  example.com/x1: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/x1: v1.0.0\n  example.com/x2: v1.0.0\n"),
	})
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/y", "")})
	fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	var out bytes.Buffer
	if err := Why(ctx, fx.session(t, "."), &out, "example.com/y", "example.com/none"); err != nil {
		t.Fatalf("Why: %v", err)
	}
	want := "# example.com/y\nexample.com/m\nexample.com/x1@v1.0.0\nexample.com/x1@v1.0.0 => example.com/y@v1.0.0\nexample.com/m\nexample.com/x2@v1.0.0\nexample.com/x2@v1.0.0 => example.com/y@v1.0.0\n\n# example.com/none\n(module example.com/none is not needed)\n"
	if out.String() != want {
		t.Fatalf("why = %q, want %q", out.String(), want)
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

// The directory form: the workspace replaces example.com/x with a
// directory of the working tree, read as a workspace module is — its
// module file the requirements of every x node, its files x's, at
// whatever state the working copy is in — never fetched, never pinned,
// the trust policy never consulted; a pin under x from before is
// pruned as unreachable, and every verb names the directory
// (REQ-work-replace-dir, REQ-work-replace-names, REQ-dep-download,
// REQ-dep-tidy, REQ-dep-why, REQ-dep-update).
func TestReplaceByDirectory(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":         "use:\n  - m\nreplace:\n  example.com/x: ./forks/x\n",
		"m/pb.yaml":       ws("example.com/m", "  example.com/a: v1.0.0\n"),
		"m/m.proto":       "syntax = \"proto3\";\nimport \"a.proto\";\n",
		"forks/x/pb.yaml": ws("example.com/x", "  example.com/z: v1.0.0\n"),
		"forks/x/x.proto": "syntax = \"proto3\";\n// the fork\n",
		// A nested module's files are the nested module's, not x's.
		"forks/x/inner/pb.yaml":     ws("example.com/inner", ""),
		"forks/x/inner/inner.proto": "syntax = \"proto3\";\n",
	})
	fx.serve(t, "example.com/a", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/a", "  example.com/x: v1.0.0\n"),
		"a.proto": "syntax = \"proto3\";\nimport \"x.proto\";\n",
	})
	fx.serve(t, "example.com/z", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/z", ""),
		"z.proto": "syntax = \"proto3\";\n",
	})
	// The original is served, unlike the fork test's: a build that
	// fetched x would read "the original" and be refused by the rule
	// below, which requires provenance x's origin does not serve.
	fx.serve(t, "example.com/x", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/x", ""),
		"x.proto": "syntax = \"proto3\";\n// the original\n",
	})
	for _, served := range []string{"example.com/a", "example.com/x", "example.com/z"} {
		fx.Endpoint(served, "v1.0.0", "info", `{"version":"v1.0.0"}`)
	}
	s := fx.session(t, ".")
	// A rule requiring provenance of x never fires: nothing of x is
	// fetched or judged, the working tree being the workspace's own.
	s.Client.Policy = &trust.Policy{Modules: []trust.Rule{{Prefix: "example.com/x", Require: trust.RequireProvenance}}}
	list, mods, err := s.Modules(ctx)
	if err != nil {
		t.Fatalf("Modules: %v", err)
	}
	selected := map[string]string{}
	for _, r := range list {
		selected[r.Path] = r.Version.String()
	}
	if selected["example.com/x"] != "v1.0.0" || selected["example.com/z"] != "v1.0.0" || len(list) != 3 {
		t.Fatalf("build list = %v, want a, x (read through the directory) and the directory's own requirement z", selected)
	}
	files := func(mods []modfiles.Module) map[string][]byte {
		for _, m := range mods {
			if m.Path == "example.com/x" {
				if m.Local || m.Synthesized || m.Version != "v1.0.0" || m.Dir != "forks/x" || m.Label() != "example.com/x@v1.0.0 => ./forks/x" {
					t.Fatalf("x = %+v: a directory replacement is an external of the build at x's selected version, named with its directory", m)
				}
				return m.Files
			}
		}
		t.Fatal("x is not in the build")
		return nil
	}
	got := files(mods)
	if !strings.Contains(string(got["x.proto"]), "the fork") || len(got) != 1 {
		t.Fatalf("x's files = %v, want the directory's alone, the nested module's excluded", got)
	}
	if _, ok := s.Lock.Module("example.com/x", "v1.0.0"); ok {
		t.Fatal("the replaced path was pinned: a directory replacement records nothing")
	}
	if n := len(s.Lock.Modules); n != 2 {
		t.Fatalf("%d pins, want a and z alone", n)
	}
	// Whatever state the working copy is in: an edit is the next
	// build's input.
	fx.write(t, "forks/x/x.proto", "syntax = \"proto3\";\n// the fork, edited\n")
	_, mods, err = fx.session(t, ".").Modules(ctx)
	if err != nil {
		t.Fatalf("Modules after the edit: %v", err)
	}
	if !strings.Contains(string(files(mods)["x.proto"]), "the fork, edited") {
		t.Fatal("x's files are not the working copy's current state")
	}
	// A malformed file of the directory is named by its place in the
	// tree, where the user can fix it.
	fx.write(t, "forks/x/x.proto", "syntax = \"proto3\";\nmessage {\n")
	if err := Tidy(ctx, fx.session(t, "."), io.Discard); err == nil || !strings.Contains(err.Error(), "forks/x/x.proto") {
		t.Fatalf("tidy over a malformed fork file: err = %v, want the file named by its tree path", err)
	}
	fx.write(t, "forks/x/x.proto", "syntax = \"proto3\";\n// the fork\n")

	// The export report names the module as every report does
	// (REQ-export-report, REQ-work-replace-dir).
	var out bytes.Buffer
	if err := Export(ctx, s, "exported", "exported", ExportOptions{}, &out); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/x@v1.0.0 => ./forks/x: 1 file(s)\n") {
		t.Fatalf("export report = %q, want the directory replacement rendered with its directory", out.String())
	}
	out.Reset()
	if err := Download(ctx, s, &out); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/x@v1.0.0 => ./forks/x\n") {
		t.Fatalf("download lines = %q, want the replaced module named with its directory", out.String())
	}
	out.Reset()
	if err := Graph(ctx, s, &out); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/x@v1.0.0 example.com/z@v1.0.0\n") {
		t.Fatalf("graph = %q, want x's edges read from the directory's module file", out.String())
	}
	out.Reset()
	if err := Why(ctx, s, &out, "example.com/x"); err != nil {
		t.Fatalf("Why: %v", err)
	}
	if want := "# example.com/x\nexample.com/m\nexample.com/a@v1.0.0\nexample.com/x@v1.0.0\nexample.com/x@v1.0.0 => ./forks/x\n"; out.String() != want {
		t.Fatalf("why = %q, want %q", out.String(), want)
	}

	// Tidy leaves the declaration on x's requirer and prunes a pin
	// under x from before the replacement: it is outside the graph.
	before := fx.read(t, "m/pb.yaml")
	s2 := fx.session(t, ".")
	s2.Client.Cache = s.Client.Cache
	s2.Lock.Modules = append(s2.Lock.Modules, lockfile.ModulePin{Path: "example.com/x", Version: "v1.0.0", Digest: "pb1:" + strings.Repeat("00", 32)})
	if err := Tidy(ctx, s2, io.Discard); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if got := fx.read(t, "m/pb.yaml"); got != before {
		t.Fatalf("tidy rewrote m/pb.yaml to %q", got)
	}
	if _, ok := s2.Lock.Module("example.com/x", "v1.0.0"); ok {
		t.Fatal("tidy kept a pin under the replaced path: a directory replacement has none")
	}

	// An export cannot exclude x: the build read the directory, which
	// no pin under x names for the consumer to hold to.
	if err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{Exclude: []string{"example.com/x"}}, io.Discard); err == nil || !strings.Contains(err.Error(), "replaced path, read from ./forks/x") {
		t.Fatalf("export excluding a replaced path: err = %v, want the refusal naming the source", err)
	}

	// Update never discovers x: the sweep reports the directory.
	fx.write(t, "m/pb.yaml", ws("example.com/m", "  example.com/a: v1.0.0\n  example.com/x: v1.0.0\n"))
	fx.Endpoints[fetchtest.ProxyHost+"/example.com/a/@v/list"] = []byte("v1.0.0\n")
	out.Reset()
	if err := Update(ctx, fx.session(t, "."), &out, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/x: replaced by ./forks/x, declaration left\n") {
		t.Fatalf("update lines = %q, want the directory named", out.String())
	}
	if err := Update(ctx, fx.session(t, "."), io.Discard, nil, "example.com/x"); err == nil || !strings.Contains(err.Error(), "replaced by ./forks/x") {
		t.Fatalf("named update of a directory-replaced path: err = %v", err)
	}
}

// A malformed file of a pinned replacement is named by the replaced
// module's one rendering, the pair whose bytes the file is beside
// the requirement it stands for (workspace.md REQ-work-replace): the
// requirement's pair alone names nothing fetched.
func TestTidyNamesAReplacementsMalformedFileByItsSource(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/x: v1.0.0\n"),
		"m/m.proto": "syntax = \"proto3\";\nimport \"x.proto\";\n",
	})
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/y", ""),
		"x.proto": "syntax = \"proto3\";\nmessage {\n",
	})
	fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	err := Tidy(ctx, fx.session(t, "."), io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "example.com/x@v1.0.0 => example.com/y@v1.0.0: x.proto: ") {
		t.Fatalf("a malformed replacement file: %v", err)
	}
}

// A ruleset import a pair replacement stands for is pinned under the
// replacement's pair, and the read-only session's unpinned list names
// that pair — never the import's, which no pin ever carries — so the
// language server reports the pair the download verb pins and
// judges once it is pinned (lsp.md REQ-lsp-unpinned, workspace.md
// REQ-work-replace).
func TestUnpinnedNamesAReplacedRulesetBySource(t *testing.T) {
	rules := "celEnv: 1\nrules:\n  - id: PACKAGE_DEFINED\n    kind: lint\n    target: file\n    severity: error\n    cel: file.package != ''\n    message: files declare a package\n"
	fx := newDep(t, map[string]string{
		"pb.work":      "use:\n  - m\nreplace:\n  example.com/std: example.com/fork@v2.0.0\n",
		"pb.lint.yaml": "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\nenable: [std:PACKAGE_DEFINED]\n",
		"m/pb.yaml":    ws("example.com/m", ""),
		"m/m.proto":    "syntax = \"proto3\";\npackage m;\n",
	})
	fx.serve(t, "example.com/fork", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/fork", ""), "std.rules.yaml": rules})
	fx.Endpoint("example.com/fork", "v2.0.0", "info", `{"version":"v2.0.0"}`)
	s, err := Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy"), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := s.Unpinned()
	if err != nil || len(pairs) != 1 || pairs[0].String() != "example.com/fork@v2.0.0" {
		t.Fatalf("the unpinned pairs before the download: %v, %v", pairs, err)
	}
	if err := Download(ctx, fx.session(t, "."), io.Discard); err != nil {
		t.Fatalf("Download: %v", err)
	}
	s, err = Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy"), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if pairs, err := s.Unpinned(); err != nil || len(pairs) != 0 {
		t.Fatalf("the unpinned pairs after the download: %v, %v", pairs, err)
	}
	if j, err := Judge(ctx, s, nil); err != nil || !j.Compiles() {
		t.Fatalf("the read-only judgement over the replaced ruleset: %v, %v", j, err)
	}
}

// A replaced module whose replacement cannot be fetched is named in
// the failure by the one rendering: the requirement and the
// replacement, so the user sees what the fetched pair stands for
// (workspace.md REQ-work-replace).
func TestDownloadNamesAReplacedModuleInItsFailure(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v9.9.9\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/x: v1.0.0\n"),
		"m/m.proto": "syntax = \"proto3\";\nimport \"x.proto\";\n",
	})
	// The replacement's module file unreachable: the requirement
	// loader's failure.
	err := Download(ctx, fx.session(t, "."), io.Discard)
	if err == nil || !strings.Contains(err.Error(), ": example.com/x@v1.0.0 => example.com/y@v9.9.9: ") {
		t.Fatalf("Download over an unreachable module file: %v", err)
	}
	// Pinned, then the archive withdrawn from the source and the
	// cache fresh: the download's own failure, named the same way.
	fx.serve(t, "example.com/y", "v9.9.9", map[string]string{"pb.yaml": ws("example.com/y", ""), "x.proto": "syntax = \"proto3\";\n"})
	fx.Endpoint("example.com/y", "v9.9.9", "info", `{"version":"v9.9.9"}`)
	fx.Endpoint("example.com/y", "v9.9.9", "mod", ws("example.com/y", ""))
	if err := Download(ctx, fx.session(t, "."), io.Discard); err != nil {
		t.Fatalf("Download: %v", err)
	}
	delete(fx.Endpoints, fx.Endpoint("example.com/y", "v9.9.9", "zip", ""))
	err = Download(ctx, fx.session(t, "."), io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "example.com/x@v1.0.0 => example.com/y@v9.9.9: ") || !strings.Contains(err.Error(), "v9.9.9.zip") {
		t.Fatalf("Download over a withdrawn archive: %v", err)
	}
}

// A pair replacement's archive declares the replacement's own path,
// the name it is fetched, verified and pinned under: a fork whose
// module file still declares the replaced path is refused at the
// download, naming the mismatch (workspace.md REQ-work-replace,
// module-file.md REQ-modfile-identity).
func TestReplacementDeclaringTheReplacedPathIsRefused(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n",
		"m/pb.yaml": ws("example.com/m", "  example.com/x: v1.0.0\n"),
		"m/m.proto": "syntax = \"proto3\";\nimport \"x.proto\";\n",
	})
	fx.serve(t, "example.com/y", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/x", ""), "x.proto": "syntax = \"proto3\";\n"})
	fx.Endpoint("example.com/y", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	err := Download(ctx, fx.session(t, "."), io.Discard)
	if !errors.Is(err, modfile.ErrIdentityMismatch) || !strings.Contains(err.Error(), "example.com/x@v1.0.0 => example.com/y@v1.0.0") || !strings.Contains(err.Error(), `declares "example.com/x", required as "example.com/y"`) {
		t.Fatalf("Download over a fork declaring the replaced path: %v", err)
	}
}
