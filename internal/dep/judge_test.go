package dep

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/source/fetch"
)

// Judge is the lint verb's judgement: over the same tree its
// findings are the verb's, one to one; the overlay's bytes stand in
// for the tree's; a build that does not compile carries every error
// and no finding (lsp.md REQ-lsp-parity, REQ-lsp-overlay).
func TestJudgeMatchesLint(t *testing.T) {
	fx := newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\nignore:\n  - paths: [\"vendor/**\"]\n")
	var out strings.Builder
	if err := Lint(ctx, fx.session(t, "."), &out, io.Discard); !errors.Is(err, ErrFindings) {
		t.Fatalf("Lint: %v", err)
	}
	j, err := Judge(ctx, fx.session(t, "."), nil)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !j.Compiles() || j.Rules == 0 || len(j.Files) == 0 {
		t.Fatalf("the judgement: compiles %v, rules %d, files %d", j.Compiles(), j.Rules, len(j.Files))
	}
	check.Sort(j.Findings)
	var lines []string
	for _, f := range j.Findings {
		lines = append(lines, f.String())
	}
	if got := strings.Join(lines, "\n") + "\n"; got != out.String() {
		t.Fatalf("Judge:\n%spb lint:\n%s", got, out.String())
	}
	// The overlay replaces a file's bytes and adds a file the tree
	// does not hold; both judged as the module's.
	overlay := Overlay{
		"a/a.proto":   []byte("syntax = \"proto3\";\npackage a;\n\nmessage Thing {\n  string fine = 1;\n}\n"),
		"a/new.proto": []byte("syntax = \"proto3\";\npackage a;\nmessage New {\n  string AlsoBad = 1;\n}\n"),
	}
	j, err = Judge(ctx, fx.session(t, "."), overlay)
	if err != nil {
		t.Fatalf("Judge with an overlay: %v", err)
	}
	check.Sort(j.Findings)
	lines = nil
	for _, f := range j.Findings {
		lines = append(lines, f.String())
	}
	// The root's set rule counts the vendored message too: an ignore
	// drops findings by path, and a set finding has none.
	if got := strings.Join(lines, "\n"); got != "b.proto:6:3: error house:FIELD_NAMES: field names are snake_case\nnew.proto:4:3: error house:FIELD_NAMES: field names are snake_case\nwarning house:MESSAGE_COUNT: too many messages" {
		t.Fatalf("the overlaid judgement:\n%s", got)
	}
	// A build that does not compile: every error, no finding.
	overlay["a/a.proto"] = []byte("syntax = \"proto3\";\npackage a;\nmessage Thing {\n  string x = 1\n}\n")
	overlay["b/b.proto"] = []byte("syntax = \"proto3\";\npackage b;\nimport \"nowhere.proto\";\nmessage Use {\n  string Loud = 2;\n}\n")
	j, err = Judge(ctx, fx.session(t, "."), overlay)
	if err != nil {
		t.Fatalf("Judge over a broken build: %v", err)
	}
	if j.Compiles() || len(j.Compile) != 2 || len(j.Findings) != 0 {
		t.Fatalf("the broken build's judgement: %+v", j)
	}
}

// A read-only session resolves at the lockfile's pins alone: a pair
// the lockfile does not pin is refused naming it, nothing is fetched
// for it and no lockfile is written; Unpinned lists the declared
// pairs ahead, each once (lsp.md REQ-lsp-session, REQ-lsp-unpinned).
func TestReadOnlySession(t *testing.T) {
	fx := newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\n")
	// A second declared module, so the module list and the ruleset
	// list each contribute a pair of their own.
	fx.serve(t, "example.com/other", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/other", ""), "other.proto": "syntax = \"proto3\";\npackage other;\n"})
	fx.write(t, "b/pb.yaml", ws("example.com/b", "  example.com/a: v0.0.1\n  example.com/other: v1.0.0\n"))
	s, err := Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy"), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := s.Unpinned()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].String() != "example.com/other@v1.0.0" || pairs[1].String() != "example.com/std@v1.0.0" {
		t.Fatalf("the unpinned pairs: %v", pairs)
	}
	// The resolution refuses the first unpinned pair it meets, which
	// names the pair and the verb that pins it.
	_, err = Judge(ctx, s, nil)
	var unpinned *fetch.UnpinnedError
	if !errors.As(err, &unpinned) || unpinned.Path != "example.com/other" || unpinned.Version != "v1.0.0" || !strings.Contains(err.Error(), "pb dep download") {
		t.Fatalf("a read-only judgement over an unpinned pair: %v", err)
	}
	if pair, ok := UnpinnedIn(err); !ok || pair.Path != "example.com/other" {
		t.Fatalf("UnpinnedIn: %v %v", pair, ok)
	}
	if _, err := fx.ws.Stat("pb.lock"); err == nil {
		t.Fatal("a read-only session wrote the lockfile")
	}
	// Pinned by a verb, the read-only session judges, and a lockfile
	// spelled non-canonically is left as it is.
	if err := Lint(ctx, fx.session(t, "."), io.Discard, io.Discard); !errors.Is(err, ErrFindings) {
		t.Fatalf("Lint: %v", err)
	}
	lock := fx.read(t, "pb.lock") + "# a trailing comment\n"
	fx.write(t, "pb.lock", lock)
	s, err = Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy"), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if pairs, err := s.Unpinned(); err != nil || len(pairs) != 0 {
		t.Fatalf("the unpinned pairs after pinning: %v, %v", pairs, err)
	}
	if j, err := Judge(ctx, s, nil); err != nil || !j.Compiles() || len(j.Findings) == 0 {
		t.Fatalf("the read-only judgement after pinning: %v, %v", j, err)
	}
	if got := fx.read(t, "pb.lock"); got != lock {
		t.Fatalf("the lockfile was rewritten: %q", got)
	}
	if s.BuildHome() != "pb.work" || s.LockPath() != "pb.lock" || s.ModuleFile("a") != "a/pb.yaml" {
		t.Fatalf("the session's files: %s %s %s", s.BuildHome(), s.LockPath(), s.ModuleFile("a"))
	}
}

// treeDigest hashes every file of the fixture's tree.
func (fx *depFixture) treeDigest(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	var paths []string
	fsys := iofs.New(fx.ws)
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s\n%d\n", p, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// protoFile generates a protobuf file of the given package: messages
// with fields whose names are snake_case or not, so the lint verb's
// verdict varies with the draw; a tab in the indentation now and
// then, which the column count reads as one point.
func protoFile(rt *rapid.T, label, pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "syntax = \"proto3\";\npackage %s;\n", pkg)
	n := rapid.IntRange(1, 3).Draw(rt, label+"-messages")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nmessage M%d {\n", i)
		fields := rapid.IntRange(0, 3).Draw(rt, fmt.Sprintf("%s-m%d-fields", label, i))
		for j := 0; j < fields; j++ {
			indent := "  "
			if rapid.Bool().Draw(rt, fmt.Sprintf("%s-m%d-f%d-tab", label, i, j)) {
				indent = "\t"
			}
			name := rapid.SampledFrom([]string{"ok_name", "BadName", "fine", "alsoBad", "x_y_z"}).Draw(rt, fmt.Sprintf("%s-m%d-f%d-name", label, i, j))
			fmt.Fprintf(&b, "%sstring %s = %d;\n", indent, name, j+1)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// Over any tree, the judgement's findings are the lint verb's, one
// to one — the same files, lines and columns, severities, rules and
// messages — and so are its verdict on a build that does not compile
// (REQ-lsp-parity).
func TestJudgeParityProperty(t *testing.T) {
	fx := newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nignore:\n  - paths: [\"vendor/**\"]\n")
	if err := Lint(ctx, fx.session(t, "."), io.Discard, io.Discard); err != nil && !errors.Is(err, ErrFindings) {
		t.Fatalf("pinning through lint: %v", err)
	}
	rapid.Check(t, func(rt *rapid.T) {
		fx.write(t, "a/a.proto", protoFile(rt, "a", "a"))
		b := protoFile(rt, "b", "b")
		if rapid.Bool().Draw(rt, "b-broken") {
			b = strings.Replace(b, "\n}\n", "\n", 1)
		}
		fx.write(t, "b/b.proto", b)
		var out strings.Builder
		verbErr := Lint(ctx, fx.session(t, "."), &out, io.Discard)
		j, err := Judge(ctx, fx.session(t, "."), nil)
		if err != nil {
			rt.Fatalf("Judge: %v", err)
		}
		if verbErr != nil && !errors.Is(verbErr, ErrFindings) {
			// The verb failed naming what failed: the build does not
			// compile, the judgement says so with no finding, and the
			// file the verb names is among the files it names.
			if j.Compiles() || len(j.Findings) != 0 {
				rt.Fatalf("the verb failed (%v) and the judgement compiled: %+v", verbErr, j.Findings)
			}
			named := ""
			for _, word := range strings.FieldsFunc(verbErr.Error(), func(r rune) bool { return r == ' ' || r == ':' || r == '"' }) {
				if strings.HasSuffix(word, ".proto") {
					named = word
					break
				}
			}
			found := named == ""
			for _, e := range j.Compile {
				found = found || e.Path == named
			}
			if !found {
				rt.Fatalf("the verb named %s (%v); the judgement's errors: %v", named, verbErr, j.Compile)
			}
			return
		}
		if !j.Compiles() {
			rt.Fatalf("the verb judged and the judgement did not compile: %v", j.Compile)
		}
		check.Sort(j.Findings)
		var lines []string
		for _, f := range j.Findings {
			lines = append(lines, f.String())
		}
		got := strings.Join(lines, "\n")
		if len(lines) != 0 {
			got += "\n"
		}
		if got != out.String() {
			rt.Fatalf("Judge:\n%spb lint:\n%s", got, out.String())
		}
	})
}

// A read-only session writes nothing whatever it judges: any overlay,
// a build that compiles or not, pinned or unpinned — the tree's every
// byte is as it was, and an unpinned pair is refused by name
// (REQ-lsp-tree-untouched, REQ-lsp-session).
func TestReadOnlyNeverWrites(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		fx := newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\n")
		pinned := rapid.Bool().Draw(rt, "pinned")
		if pinned {
			if err := Lint(ctx, fx.session(t, "."), io.Discard, io.Discard); err != nil && !errors.Is(err, ErrFindings) {
				rt.Fatalf("pinning through lint: %v", err)
			}
			// A lockfile spelled otherwise than canonically is left
			// as it is.
			fx.write(t, "pb.lock", fx.read(t, "pb.lock")+"# kept\n")
		}
		before := fx.treeDigest(t)
		overlay := Overlay{}
		for _, p := range []string{"a/a.proto", "b/b.proto", "a/fresh.proto"} {
			if rapid.Bool().Draw(rt, p+"-overlaid") {
				overlay[p] = []byte(protoFile(rt, p, strings.TrimSuffix(path.Base(p), ".proto")))
			}
		}
		s, err := Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy"), ReadOnly: true})
		if err != nil {
			rt.Fatal(err)
		}
		_, err = Judge(ctx, s, overlay)
		pair, unpinned := UnpinnedIn(err)
		if unpinned == pinned || (unpinned && pair.String() != "example.com/std@v1.0.0") {
			rt.Fatalf("pinned %v, judged: %v", pinned, err)
		}
		if err != nil && !unpinned {
			rt.Fatalf("Judge: %v", err)
		}
		if after := fx.treeDigest(t); after != before {
			rt.Fatal("the read-only session wrote to the tree")
		}
	})
}

// Wire sets the client from the session: a reader keeping a session
// after a later load rewired the shared client reads its own pins,
// policy and mode again (lsp.md REQ-lsp-reload).
func TestSessionWire(t *testing.T) {
	fx := newCheck(t, "")
	client := fx.client("proxy")
	first, err := Load(Config{WS: fx.ws, Dir: ".", Client: client, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(Config{WS: fx.ws, Dir: ".", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if client.Lock != second.Lock || client.Lock == first.Lock || client.ReadOnly {
		t.Fatal("the second load did not wire the client")
	}
	first.Wire()
	if client.Lock != first.Lock || !client.ReadOnly || client.Policy != first.policy || first.Driver.Client != client {
		t.Fatal("Wire did not set the client from the kept session")
	}
}
