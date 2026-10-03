package lsp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"pgregory.net/rapid"
)

// The dependency source store is filled from the first judgement that
// reads the build after the session's load, not the load alone: a
// judgement at the load that cannot read a dependency leaves no copy,
// and the next judgement that reads it, without a reload, fills the
// store, so the addresses handed out name files that exist
// (REQ-lsp-dependency-files).
func TestSourceStoreFilledAtTheFirstJudgementThatReads(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	zip, _ := fetchtest.ModuleZip(t, stdModule())
	delete(fx.Endpoints, fx.Endpoint("example.com/std", "v1.0.0", "zip", ""))
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.message(t, "the build could not be judged")
	copy := filepath.Join(fx.sources, "example.com", "std@v1.0.0", "std.proto")
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatalf("a copy before any judgement read the dependency: %v", err)
	}
	// The dependency served again: an edit's judgement, no reload,
	// reads it and fills the store.
	fx.Endpoint("example.com/std", "v1.0.0", "zip", string(zip))
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.until(t, "the copy", func() bool { _, err := os.Stat(copy); return err == nil })
	if b, err := os.ReadFile(copy); err != nil || string(b) != stdModule()["std.proto"] {
		t.Fatalf("the copy: %q, %v", b, err)
	}
	// Filled once per load: a copy gone is not restored by an edit's
	// judgement, and is by the next load's.
	if err := os.Remove(copy); err != nil {
		t.Fatal(err)
	}
	fx.change(t, "ws/a/a.proto", 2, string(fx.text(t, "ws/a/a.proto"))+"\n")
	fx.publishFor(t, "the edit's judgement", func(p *protocol.PublishDiagnosticsParams) bool {
		v, _ := p.Version.Get()
		return p.URI == fx.uri("ws/a/a.proto") && v == 2
	})
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatalf("an edit's judgement refilled the store: %v", err)
	}
	fx.watched(t, "ws/pb.lock", protocol.FileChangeTypeChanged)
	fx.until(t, "the copy restored by the load", func() bool { _, err := os.Stat(copy); return err == nil })
}

// A filling that fails is tried again at the next judgement and
// reported once while the same failure stands; a store writable
// again is filled (REQ-lsp-dependency-files).
func TestSourceStoreFilledAgainAfterAFailedFilling(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	dir := filepath.Join(fx.sources, "example.com", "std@v1.0.0")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file where the copy's directory goes: the filling
	// fails at the first judgement.
	if err := os.WriteFile(dir, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.message(t, "the dependency source store could not be filled")
	// The same failure at the next judgement is logged, not shown
	// again; the way cleared, the next judgement fills the store.
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.publishes(t, "ws/a/a.proto")
	fx.change(t, "ws/a/a.proto", 2, string(fx.text(t, "ws/a/a.proto"))+"\n")
	fx.publishFor(t, "the edit's judgement", func(p *protocol.PublishDiagnosticsParams) bool {
		v, _ := p.Version.Get()
		return p.URI == fx.uri("ws/a/a.proto") && v == 2
	})
	select {
	case m := <-fx.client.messages:
		t.Fatalf("the standing failure shown again: %q", m)
	default:
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	fx.change(t, "ws/a/a.proto", 3, string(fx.text(t, "ws/a/a.proto"))+"\n\n")
	copy := filepath.Join(dir, "std.proto")
	fx.until(t, "the copy after the way cleared", func() bool { _, err := os.Stat(copy); return err == nil })
}

// A copy present and equal is made read-only where it is not — a
// store filled before the mode, or a copy chmod'ed and reverted
// (REQ-lsp-dependency-files).
func TestSourceStoreMakesAnEqualCopyReadOnly(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	copy := filepath.Join(fx.sources, "example.com", "std@v1.0.0", "std.proto")
	if err := os.MkdirAll(filepath.Dir(copy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copy, []byte(stdModule()["std.proto"]), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.until(t, "the copy made read-only", func() bool { fi, err := os.Stat(copy); return err == nil && fi.Mode().Perm()&0o222 == 0 })
}

// A directory where a copy's file goes is left as it is — never made
// writable for a replacement, which a regular file alone is — and
// fails the filling as its own (REQ-lsp-dependency-files).
func TestSourceStoreLeavesAStrayDirectoryAlone(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	stray := filepath.Join(fx.sources, "example.com", "std@v1.0.0", "std.proto")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	// The mode as the platform made it, whatever the umask or the
	// platform's spelling of a writable directory.
	before, err := os.Stat(stray)
	if err != nil {
		t.Fatal(err)
	}
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.message(t, "the dependency source store could not be filled")
	if fi, err := os.Stat(stray); err != nil || !fi.IsDir() || fi.Mode() != before.Mode() {
		t.Fatalf("the stray directory: %v, %v; was %v", fi, err, before.Mode())
	}
}

// A copy present and differing — the bytes under an address having
// been another build's, or edited past the mode — is replaced by the
// bytes the build read, the read-only copy made writable for the
// replacement alone (REQ-lsp-dependency-files).
func TestSourceStoreReplacesADifferingReadOnlyCopy(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	copy := filepath.Join(fx.sources, "example.com", "std@v1.0.0", "std.proto")
	if err := os.MkdirAll(filepath.Dir(copy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copy, []byte("stale"), 0o444); err != nil {
		t.Fatal(err)
	}
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.until(t, "the copy replaced", func() bool { b, err := os.ReadFile(copy); return err == nil && string(b) == stdModule()["std.proto"] })
}

// A dependency file's address decodes to the file's path for every
// path the archive admits — a `%`, a `#`, a space, an `@`, a letter
// beyond ASCII among its characters — and compares equal to the
// address the client sends back, so the content served for it is the
// file's own (REQ-lsp-dependency-files, module-archive.md
// REQ-archive-path-rules).
func TestModuleAddressRoundTripsEveryArchivePath(t *testing.T) {
	s := &Server{content: true}
	reserved := regexp.MustCompile(`^(?i)(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)
	segment := rapid.StringMatching("[A-Za-z0-9%#@ +&=;,'()\\[\\]!$~_.^`{}éü日-]{1,8}").Filter(func(seg string) bool {
		return !strings.HasSuffix(seg, " ") && !strings.HasSuffix(seg, ".") && seg != "." && seg != ".." && !reserved.MatchString(seg)
	})
	rapid.Check(t, func(t *rapid.T) {
		segs := rapid.SliceOfN(segment, 1, 3).Draw(t, "segments")
		p := strings.Join(segs, "/") + ".proto"
		s.files = &buildFiles{byPath: map[string]file{p: {origin: origin{modPath: "example.com/m", version: "v1.0.0"}, text: []byte(p)}}}
		u := s.moduleURI("example.com/m", "v1.0.0", p)
		if back := uri.MustParse(u.String()); back != u {
			t.Fatalf("the address %s is not canonical: the client would send back %s", u, back)
		}
		b, err := s.moduleContent(u)
		if err != nil || string(b) != p {
			t.Fatalf("the content for %s: %q, %v", u, b, err)
		}
	})
}
