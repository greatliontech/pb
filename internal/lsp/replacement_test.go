package lsp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// A pinned replacement's files are addressed by the pair whose bytes
// they are, the replacement's, and copied under it: a replacement
// that moves moves the address with the bytes, while the
// requirement's name stays what the module graph shows
// (REQ-lsp-dependency-files, workspace.md REQ-work-replace).
func TestReplacementAddressFollowsTheBytes(t *testing.T) {
	s, err := New(Deps{Sources: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.content = true
	files := map[string][]byte{"x.proto": []byte("syntax = \"proto3\";\npackage x;\n")}
	var addresses []string
	for _, replacement := range []string{"v1.0.0", "v2.0.0"} {
		m := modfiles.Module{Path: "example.com/x", Version: "v0.1.0", SourcePath: "example.com/y", SourceVersion: replacement, Files: files}
		if m.Label() != "example.com/x@v0.1.0 => example.com/y@"+replacement {
			t.Fatalf("the requirement's name: %s", m.Label())
		}
		table := newFiles("ws", []modfiles.Module{m}, nil)
		u := s.address(table.byPath["x.proto"].origin, "x.proto")
		if want := "pb-module://example.com/y%40" + replacement + "/x.proto"; string(u) != want {
			t.Fatalf("the address: %s, want %s", u, want)
		}
		addresses = append(addresses, string(u))
		// The source store's copy lands under the same pair.
		if err := s.copySources([]modfiles.Module{m}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(filepath.Join(s.deps.Sources, "example.com", "y@"+replacement, "x.proto")); err != nil {
			t.Fatalf("the copy under the source pair: %v", err)
		}
		if _, err := os.Stat(filepath.Join(s.deps.Sources, "example.com", "x@v0.1.0")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a copy under the requirement's pair: %v", err)
		}
	}
	if addresses[0] == addresses[1] {
		t.Fatalf("the address stayed while the bytes moved: %s", addresses[0])
	}
	// The address names no requirement: a module file addressed
	// under the replaced pair would name different bytes across
	// reloads.
	for _, a := range addresses {
		if strings.Contains(a, "example.com/x") {
			t.Fatalf("the address names the requirement: %s", a)
		}
	}
}
