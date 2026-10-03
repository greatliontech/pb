package migrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

// tree is an in-memory working tree with files at paths.
func tree(t *testing.T, files map[string]string) billy.Filesystem {
	t.Helper()
	ws := memfs.New()
	for p, text := range files {
		if err := util.WriteFile(ws, p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

func readTree(t *testing.T, ws billy.Filesystem, p string) string {
	t.Helper()
	b, err := util.ReadFile(ws, p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return string(b)
}

// A configuration below the root (--config): pb's files land at the
// root, the module file at the module's rooted directory, the lint
// file keyed by it, a lone module below the root a workspace of one,
// an output directory above the configuration read relative to the
// root, the comments rewritten under the configuration
// (REQ-migrate-verb, REQ-migrate-modules, REQ-migrate-gen).
func TestRunConfigBelowRoot(t *testing.T) {
	ws := tree(t, map[string]string{
		"repo/proto/buf.yaml":     "version: v2\nmodules:\n  - path: .\nlint:\n  use: [STANDARD]\n  ignore: [acme/legacy.proto]\n",
		"repo/proto/buf.gen.yaml": "version: v2\ninputs:\n  - directory: .\n    paths: [acme]\nplugins:\n  - remote: buf.build/protocolbuffers/go:v1.35.2\n    out: ../gen/go\n",
		"repo/proto/acme/a.proto": "syntax = \"proto3\";\npackage acme;\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage m {}\n",
		"repo/README.md":          "kept",
	})
	var out strings.Builder
	inv := Invocation{
		WS: ws, Dir: "repo", Config: "proto", ModulePath: "github.com/acme/repo/proto",
		Discovery: &fakeDiscovery{latest: map[string]string{Ruleset: "v0.1.0"}},
		Tidy:      func(context.Context) error { return nil },
		Out:       &out,
	}
	if err := Run(context.Background(), inv); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	for _, line := range []string{
		"buf.yaml modules[0] . -> proto/pb.yaml module: github.com/acme/repo/proto\n",
		"buf.yaml.lint.ignore acme/legacy.proto -> ignore paths [acme/legacy.proto/**] kind lint\n",
		"buf.gen.yaml plugins[0].out ../gen/go -> out: gen/go\n",
		"buf.gen.yaml inputs[0].paths[0] acme -> files: acme/**\n",
		"proto/pb.yaml -> written\npb.work -> written\npb.lint.yaml -> written\npb.gen.yaml -> written\n",
		"proto/acme/a.proto -> 1 suppression comments rewritten to pb:ignore\n",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("the report lacks %q:\n%s", line, out.String())
		}
	}
	if got := readTree(t, ws, "repo/pb.work"); got != "use:\n  - proto\n" {
		t.Errorf("the workspace file: %q", got)
	}
	if got := readTree(t, ws, "repo/proto/pb.yaml"); !strings.HasPrefix(got, "module: github.com/acme/repo/proto\n") {
		t.Errorf("the module file: %q", got)
	}
	// A lone module's selection is the root's, its ignore paths
	// module-relative there, as for a lone module at the root.
	if got := readTree(t, ws, "repo/pb.lint.yaml"); strings.Contains(got, "modules:") || !strings.Contains(got, "ignore:\n  - paths:\n      - acme/legacy.proto/**\n    kind: lint\n") {
		t.Errorf("the lint file:\n%s", got)
	}
	if got := readTree(t, ws, "repo/pb.gen.yaml"); !strings.Contains(got, "    out: gen/go\n") || !strings.Contains(got, "      - acme/**\n") {
		t.Errorf("the generation file:\n%s", got)
	}
	if got := readTree(t, ws, "repo/proto/acme/a.proto"); !strings.Contains(got, "// pb:ignore MESSAGE_PASCAL_CASE\n") {
		t.Errorf("the comment: %q", got)
	}
	for _, p := range []string{"repo/proto/pb.work", "repo/proto/pb.lint.yaml", "repo/proto/pb.gen.yaml"} {
		if _, err := ws.Stat(p); err == nil {
			t.Errorf("%s written under the configuration", p)
		}
	}
	// A configuration directory escaping the root — holding a
	// configuration there or not — or one holding none, fails naming
	// the flag or the directory.
	for c, msg := range map[string]string{"../elsewhere": "--config", "/elsewhere": "--config", "nowhere": "no buf configuration at repo/nowhere"} {
		inv.Config = c
		inv.WS = tree(t, map[string]string{"repo/README.md": "", "elsewhere/buf.yaml": "version: v2\n"})
		if err := Run(context.Background(), inv); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("--config %s: %v", c, err)
		}
	}
}

// The verb over a v2 configuration (REQ-migrate-verb,
// REQ-migrate-report): the files written beside it, the report one
// line per fact in the steps' order, the comments rewritten where buf
// honored them, the tidy run after, the status unmapped where any
// fact was.
func TestRun(t *testing.T) {
	ws := tree(t, map[string]string{
		"repo/buf.yaml":        "version: v2\nmodules:\n  - path: proto/a\n  - path: proto/b\n    lint:\n      use: [BASIC]\ndeps:\n  - buf.build/prometheus/client-model\nlint:\n  use: [STANDARD]\n  ignore: [proto/a/gen]\n  disallow_comment_ignores: true\nbreaking:\n  use: [FILE]\nunknown: 1\n",
		"repo/buf.gen.yaml":    "version: v2\nplugins:\n  - remote: buf.build/protocolbuffers/go:v1.35.2\n    out: gen/go\n",
		"repo/buf.lock":        "version: v2\ndeps:\n  - name: buf.build/prometheus/client-model\n    commit: abc\n    digest: b5:1\n",
		"repo/proto/a/a.proto": "syntax = \"proto3\";\npackage a;\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage m {}\n",
		"repo/proto/b/b.proto": "syntax = \"proto3\";\npackage b;\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage m {}\n",
		"repo/other.txt":       "kept",
	})
	var out strings.Builder
	tidied := 0
	inv := Invocation{
		WS: ws, Dir: "repo", ModulePath: "github.com/acme/repo",
		Discovery: &fakeDiscovery{latest: map[string]string{"github.com/prometheus/client_model": "v0.6.1", Ruleset: "v0.1.0"}},
		Tidy:      func(context.Context) error { tidied++; return nil },
		Out:       &out,
	}
	err := Run(context.Background(), inv)
	if !errors.Is(err, ErrUnmapped) || tidied != 1 {
		t.Fatalf("Run: %v tidied %d", err, tidied)
	}
	report := out.String()
	for _, line := range []string{
		"buf.yaml modules[0] proto/a -> proto/a/pb.yaml module: github.com/acme/repo/proto/a\n",
		"buf.yaml deps[0] buf.build/prometheus/client-model -> github.com/prometheus/client_model@v0.6.1 (the dependency table)\n",
		"the lint file's rulesets " + Ruleset + " -> rulesets: path " + Ruleset + " version v0.1.0 alias " + RulesetAlias + " (discovered)\n",
		"buf.lock deps[0] buf.build/prometheus/client-model abc -> pinned by the tidy in pb.lock over github.com/prometheus/client_model (a BSR commit names no git commit; pb's pin is the lockfile's own)\n",
		"buf.yaml.lint.use STANDARD -> enable: " + RulesetAlias + ":STANDARD\n",
		"buf.yaml.lint.disallow_comment_ignores true -> the module's suppression comments are left as they are: buf honored none\n",
		"buf.gen.yaml plugins[0].remote buf.build/protocolbuffers/go:v1.35.2 -> ref: ghcr.io/greatliontech/pb-plugins/protocolbuffers/go:v1.35.2 (the catalog)\n",
		"buf.yaml unknown !! a key the migration does not model\n",
		"proto/a/pb.yaml -> written\nproto/b/pb.yaml -> written\npb.work -> written\npb.lint.yaml -> written\npb.gen.yaml -> written\n",
		"proto/b/b.proto -> 1 suppression comments rewritten to pb:ignore\n",
	} {
		if strings.Count(report, line) != 1 {
			t.Errorf("the report holds %q %d times:\n%s", line, strings.Count(report, line), report)
		}
	}
	if strings.Count(report, "pb.lock -> written by the tidy\n") != 0 {
		t.Errorf("a lockfile reported that no tidy wrote:\n%s", report)
	}
	// The steps' order: modules, deps, rules, gen, the keys no step
	// models, the files, the comments.
	last := -1
	for _, mark := range []string{"buf.yaml modules[0]", "buf.yaml deps[0]", "buf.yaml.lint.use", "buf.gen.yaml plugins[0]", "buf.yaml unknown", "pb.lint.yaml -> written", "proto/b/b.proto ->"} {
		at := strings.Index(report, mark)
		if at <= last {
			t.Errorf("%q out of order in:\n%s", mark, report)
		}
		last = at
	}
	// The files: module files declaring the dependencies, the
	// workspace, the lint file with b's entry, the generation file;
	// b's comment rewritten under its own section, a's left under the
	// top-level one disallowing comment ignores, buf having honored
	// none there; the buf files and the rest as they were.
	if got := readTree(t, ws, "repo/proto/a/pb.yaml"); !strings.Contains(got, "module: github.com/acme/repo/proto/a\n") || !strings.Contains(got, "  github.com/prometheus/client_model: v0.6.1\n") || strings.Contains(got, Ruleset) {
		t.Errorf("a's module file:\n%s", got)
	}
	if got := readTree(t, ws, "repo/pb.work"); got != "use:\n  - proto/a\n  - proto/b\n" {
		t.Errorf("the workspace file: %q", got)
	}
	if got := readTree(t, ws, "repo/pb.lint.yaml"); !strings.Contains(got, "modules:\n  proto/a:\n") || !strings.Contains(got, "  proto/b:\n    enable:\n      - "+RulesetAlias+":BASIC\n") {
		t.Errorf("the lint file:\n%s", got)
	}
	if got := readTree(t, ws, "repo/pb.gen.yaml"); !strings.HasPrefix(got, "plugins:\n  - ref: ghcr.io/greatliontech/pb-plugins/protocolbuffers/go:v1.35.2\n") {
		t.Errorf("the generation file:\n%s", got)
	}
	if got := readTree(t, ws, "repo/proto/b/b.proto"); !strings.Contains(got, "// pb:ignore MESSAGE_PASCAL_CASE\n") {
		t.Errorf("b's comment: %q", got)
	}
	if got := readTree(t, ws, "repo/proto/a/a.proto"); !strings.Contains(got, "// buf:lint:ignore MESSAGE_PASCAL_CASE\n") {
		t.Errorf("a's comment rewritten where buf honored none: %q", got)
	}
	for _, kept := range []string{"repo/buf.yaml", "repo/buf.gen.yaml", "repo/buf.lock", "repo/other.txt"} {
		if _, err := ws.Stat(kept); err != nil {
			t.Errorf("%s: %v", kept, err)
		}
	}

	// A second run refuses the existing files, writing nothing, before
	// any tidy; every fact mapped exits 0.
	out.Reset()
	if err := Run(context.Background(), inv); err == nil || !strings.Contains(err.Error(), "proto/a/pb.yaml exists") || tidied != 1 {
		t.Fatalf("a second run: %v", err)
	}
	ws = tree(t, map[string]string{"repo/buf.yaml": "version: v2\n"})
	inv.WS, inv.Out = ws, &out
	out.Reset()
	if err := Run(context.Background(), inv); err != nil || !strings.Contains(out.String(), "pb.lint.yaml -> written\n") || strings.Contains(out.String(), " !! ") {
		t.Fatalf("every fact mapped: %v\n%s", err, out.String())
	}
	if _, err := ws.Stat("repo/pb.work"); err == nil {
		t.Fatal("a workspace file for a lone module")
	}

	// A file existing among those to be written is refused before the
	// first write, whichever it is: b's module file present, a's is
	// not written; a lockfile at the root, or in a module's directory,
	// is refused the same, the tidy writing the one and no workspace
	// admitting the other.
	for name, present := range map[string]string{"a module file": "repo/proto/b/pb.yaml", "the root's lockfile": "repo/pb.lock", "a module's lockfile": "repo/proto/b/pb.lock"} {
		ws = tree(t, map[string]string{"repo/buf.yaml": "version: v2\nmodules:\n  - path: proto/a\n  - path: proto/b\n", present: ""})
		inv.WS = ws
		if err := Run(context.Background(), inv); err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(present, "repo/")+" exists") {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := ws.Stat("repo/proto/a/pb.yaml"); err == nil {
			t.Fatalf("%s: a's module file written before the refusal", name)
		}
	}
	// A replacement naming a plugin fails where no buf.gen.yaml lies at
	// the directory, the configuration declaring none.
	var stray Replacements
	if err := stray.Replace("plugin", "buf.build/grpc/go=ghcr.io/x/y:v1"); err != nil {
		t.Fatal(err)
	}
	inv.WS, inv.Replacements = tree(t, map[string]string{"repo/buf.yaml": "version: v2\n"}), stray
	if err := Run(context.Background(), inv); err == nil || !strings.Contains(err.Error(), "--plugin buf.build/grpc/go: no buf.gen.yaml lies at the directory") {
		t.Fatalf("a plugin replacement with no generation file: %v", err)
	}
	inv.Replacements = Replacements{}
	// The lockfile the tidy writes is reported last.
	inv.WS = tree(t, map[string]string{"repo/buf.yaml": "version: v2\n"})
	inv.Tidy = func(context.Context) error {
		return util.WriteFile(inv.WS, "repo/pb.lock", []byte("version: 1\nmodules: []\n"), 0o644)
	}
	out.Reset()
	if err := Run(context.Background(), inv); err != nil || !strings.HasSuffix(out.String(), "pb.lint.yaml -> written\npb.lock -> written by the tidy\n") {
		t.Fatalf("the tidy's lockfile: %v\n%s", err, out.String())
	}
	// A lone module below the configuration's directory makes a
	// workspace of one, the directory staying the root the tidy runs
	// at and the lockfile lands in; a lockfile in the module's
	// directory is refused before any write.
	inv.WS = tree(t, map[string]string{"repo/buf.yaml": "version: v2\nmodules:\n  - path: proto\n"})
	out.Reset()
	if err := Run(context.Background(), inv); err != nil || !strings.HasSuffix(out.String(), "pb.lock -> written by the tidy\n") || readTree(t, inv.WS, "repo/pb.work") != "use:\n  - proto\n" {
		t.Fatalf("a lone module below: %v\n%s", err, out.String())
	}
	// The root the migration wrote loads as every dep verb loads one:
	// the workspace of one at the directory, its member the module.
	if session, err := dep.Load(dep.Config{WS: inv.WS, Dir: "repo", Client: &fetch.Client{}}); err != nil || session.Root.Dir != "repo" || len(session.Root.Modules) != 1 {
		t.Fatalf("the written root through dep.Load: %v %+v", err, session)
	}
	inv.WS = tree(t, map[string]string{"repo/buf.yaml": "version: v2\nmodules:\n  - path: proto\n", "repo/proto/pb.lock": ""})
	if err := Run(context.Background(), inv); err == nil || !strings.Contains(err.Error(), "proto/pb.lock exists") {
		t.Fatalf("a lone module's lockfile present: %v", err)
	}
	inv.Tidy = func(context.Context) error { tidied++; return nil }

	// A v1 workspace: each directory's own buf.yaml read, keyed as the
	// declarations key it, a directory with none under buf's default,
	// a buf.yaml beside the workspace file read for nothing.
	ws = tree(t, map[string]string{
		"repo/buf.work.yaml": "version: v1\ndirectories:\n  - ./a\n  - b/\n",
		"repo/buf.yaml":      "version: v1\nlint:\n  use: [MINIMAL]\n",
		"repo/a/buf.yaml":    "version: v1\nname: buf.build/acme/a\nlint:\n  use: [BASIC]\n",
		"repo/a/a.proto":     "syntax = \"proto3\";\npackage a;\n",
	})
	inv.WS = ws
	out.Reset()
	err = Run(context.Background(), inv)
	report = out.String()
	if err != nil || !strings.Contains(report, "buf.work.yaml directories[0] ./a -> a/pb.yaml module: github.com/acme/repo/a\n") || !strings.Contains(report, "a/buf.yaml.lint.use BASIC -> enable: "+RulesetAlias+":BASIC\n") || !strings.Contains(report, "b/buf.yaml.lint -> enable: "+RulesetAlias+":STANDARD (buf's default, no lint section)\n") || !strings.Contains(report, "buf.yaml.lint -> nothing: beside buf.work.yaml, buf reads the directories' files alone\n") {
		t.Fatalf("a v1 workspace: %v\n%s", err, report)
	}
	if got := readTree(t, ws, "repo/pb.work"); got != "use:\n  - a\n  - b\n" {
		t.Errorf("the v1 workspace's file: %q", got)
	}
	if _, err := ws.Stat("repo/b/pb.yaml"); err != nil {
		t.Errorf("b's module file: %v", err)
	}
	// A member whose file does not parse is named with its directory.
	inv.WS = tree(t, map[string]string{"repo/buf.work.yaml": "version: v1\ndirectories: [a]\n", "repo/a/buf.yaml": "version: v2\nmodules: [{path: .}]\n"})
	if err := Run(context.Background(), inv); err == nil || !strings.Contains(err.Error(), "a/buf.yaml") {
		t.Fatalf("a member that does not parse: %v", err)
	}

	// No configuration; a file that does not parse; the origin where
	// no module path is given; a tidy that fails after the files are
	// written, its cause named.
	if err := Run(context.Background(), Invocation{WS: tree(t, map[string]string{"repo/x": ""}), Dir: "repo", ModulePath: "github.com/acme/x", Out: &out}); err == nil || !strings.Contains(err.Error(), "no buf configuration at repo") {
		t.Fatalf("no configuration: %v", err)
	}
	if err := Run(context.Background(), Invocation{WS: tree(t, map[string]string{"repo/buf.yaml": "version: v3\n"}), Dir: "repo", ModulePath: "github.com/acme/x", Out: &out}); err == nil {
		t.Fatal("a file that does not parse: no error")
	}
	if err := Run(context.Background(), Invocation{WS: tree(t, map[string]string{"repo/buf.yaml": "version: v2\n"}), Dir: "repo", HostDir: t.TempDir(), Out: &out}); err == nil || !errors.Is(err, ErrNoModulePath) {
		t.Fatalf("no module path and no origin: %v", err)
	}
	ws = tree(t, map[string]string{"repo/buf.yaml": "version: v2\n"})
	out.Reset()
	failing := Invocation{WS: ws, Dir: "repo", ModulePath: "github.com/acme/x", Discovery: &fakeDiscovery{latest: map[string]string{Ruleset: "v0.1.0"}}, Tidy: func(context.Context) error { return errors.New("no origin for the ruleset") }, Out: &out}
	err = Run(context.Background(), failing)
	if err == nil || !strings.Contains(err.Error(), "the files are written; the tidy ending the migration failed: no origin for the ruleset; finish with `pb dep tidy` once the cause is fixed") || strings.Contains(err.Error(), "pb dep update") {
		t.Fatalf("a failing tidy: %v", err)
	}
	// The ruleset's version undiscovered: the import is versionless,
	// and the failure names the update before the tidy.
	undiscovered := failing
	undiscovered.WS = tree(t, map[string]string{"repo/buf.yaml": "version: v2\n"})
	undiscovered.Discovery = &fakeDiscovery{}
	out.Reset()
	err = Run(context.Background(), undiscovered)
	if err == nil || !strings.Contains(err.Error(), "finish with `pb dep update "+Ruleset+"` once the ruleset can be reached, then `pb dep tidy`") {
		t.Fatalf("a failing tidy after an undiscovered ruleset version: %v", err)
	}
	// An unmapped fact other than the ruleset's — a dependency no
	// table names — leaves the repair the tidy's alone.
	other := failing
	other.WS = tree(t, map[string]string{"repo/buf.yaml": "version: v2\ndeps:\n  - buf.build/nobody/knows\n"})
	out.Reset()
	err = Run(context.Background(), other)
	if err == nil || !strings.Contains(err.Error(), "finish with `pb dep tidy` once the cause is fixed") || strings.Contains(err.Error(), "pb dep update") || !strings.Contains(out.String(), "buf.build/nobody/knows !!") {
		t.Fatalf("a failing tidy beside another unmapped fact: %v\n%s", err, out.String())
	}
	if _, err := ws.Stat("repo/pb.lint.yaml"); err != nil || !strings.Contains(out.String(), "pb.lint.yaml -> written") {
		t.Fatalf("the files kept and reported before the tidy: %v\n%s", err, out.String())
	}
}

// The comments of every proto file under a module buf honored them
// in are rewritten, the displaced, unplaced and block-comment
// directives reported by file and line (REQ-migrate-comments).
func TestRunComments(t *testing.T) {
	ws := tree(t, map[string]string{
		"buf.yaml":           "version: v2\n",
		"a/x.proto":          "syntax = \"proto3\";\n/* buf:lint:ignore PACKAGE_LOWER_SNAKE_CASE */\npackage A;\n// buf:lint:ignore DIRECTORY_SAME_PACKAGE\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage m {}\n",
		"a/deep/y.proto":     "syntax = \"proto3\";\npackage a;\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage n {}\n",
		"untouched.proto":    "syntax = \"proto3\";\npackage u;\nmessage U {}\n",
		"a/notes/readme.txt": "// buf:lint:ignore MESSAGE_PASCAL_CASE\nnot proto;\n",
	})
	var out strings.Builder
	err := Run(context.Background(), Invocation{WS: ws, Dir: ".", ModulePath: "github.com/acme/x", Discovery: &fakeDiscovery{latest: map[string]string{Ruleset: "v0.1.0"}}, Tidy: func(context.Context) error { return nil }, Out: &out})
	if !errors.Is(err, ErrUnmapped) {
		t.Fatalf("Run: %v", err)
	}
	want := "a/deep/y.proto -> 1 suppression comments rewritten to pb:ignore\n" +
		"a/x.proto -> 2 suppression comments rewritten to pb:ignore\n" +
		"a/x.proto:4 !! names a package or set rule, whose finding carries no position: no comment suppresses it, an ignore entry does\n" +
		"a/x.proto:2 !! a directive in a block comment, which pb reads not\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("the comment facts:\n%s", out.String())
	}
	if got := readTree(t, ws, "untouched.proto"); !strings.Contains(got, "message U") {
		t.Fatal("a file with no directive rewritten")
	}
	if got := readTree(t, ws, "a/notes/readme.txt"); got != "// buf:lint:ignore MESSAGE_PASCAL_CASE\nnot proto;\n" {
		t.Fatal("a file that is no proto rewritten")
	}
}

// From the first write on the report is printed whatever fails, the
// files written so far named and the cause wrapped: a write failing
// after the first file, a proto file unreadable at the rewrite; a
// symbolically linked proto file is left as the link it is
// (REQ-migrate-verb, REQ-migrate-comments).
func TestRunAfterWriteFailures(t *testing.T) {
	base := func() billy.Filesystem {
		return tree(t, map[string]string{
			"buf.yaml":  "version: v2\nmodules:\n  - path: a\n  - path: b\n",
			"a/x.proto": "syntax = \"proto3\";\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage m {}\n",
		})
	}
	inv := Invocation{Dir: ".", ModulePath: "github.com/acme/x", Discovery: &fakeDiscovery{latest: map[string]string{Ruleset: "v0.1.0"}}, Tidy: func(context.Context) error { return nil }}
	var out strings.Builder
	// The second module file's write fails: the first is reported
	// written, nothing after, the cause named.
	inv.WS, inv.Out = &fetchtest.ErrFS{Filesystem: base(), FailCreateDir: "b", PutFailAfter: -1}, &out
	err := Run(context.Background(), inv)
	if err == nil || !strings.Contains(err.Error(), "the files are written; writing b/pb.yaml: injected storage fault") || !strings.HasSuffix(out.String(), "a/pb.yaml -> written\n") {
		t.Fatalf("a failing write: %v\n%s", err, out.String())
	}
	// A proto file unreadable at the rewrite: the files are reported
	// written, the cause named after them.
	out.Reset()
	inv.WS = &fetchtest.ErrFS{Filesystem: base(), FailOpenSuffix: "a/x.proto", PutFailAfter: -1}
	err = Run(context.Background(), inv)
	if err == nil || !strings.Contains(err.Error(), "the files are written; the comments' rewriting failed: reading a/x.proto: injected storage fault") || !strings.HasSuffix(out.String(), "pb.lint.yaml -> written\n") {
		t.Fatalf("a failing rewrite: %v\n%s", err, out.String())
	}
	// A symbolic link to a proto file stays a link, its target as it
	// was; the regular file beside it is rewritten.
	out.Reset()
	ws := base()
	if err := util.WriteFile(ws, "elsewhere/target.proto", []byte("syntax = \"proto3\";\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nmessage t {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.Symlink("../elsewhere/target.proto", "a/link.proto"); err != nil {
		t.Fatal(err)
	}
	inv.WS = ws
	if err := Run(context.Background(), inv); err != nil || strings.Contains(out.String(), "link.proto") {
		t.Fatalf("a linked proto: %v\n%s", err, out.String())
	}
	// The target read back as the tree stores it: the host's spelling
	// (billy's chroot spells a link's target with the host's separator).
	if target, err := ws.Readlink("a/link.proto"); err != nil || filepath.ToSlash(target) != "../elsewhere/target.proto" {
		t.Fatalf("the link replaced: %q %v", target, err)
	}
	if got := readTree(t, ws, "elsewhere/target.proto"); strings.Contains(got, "pb:ignore") {
		t.Fatal("the link's target rewritten")
	}
	if got := readTree(t, ws, "a/x.proto"); !strings.Contains(got, "pb:ignore") {
		t.Fatal("the regular file beside the link not rewritten")
	}
}
