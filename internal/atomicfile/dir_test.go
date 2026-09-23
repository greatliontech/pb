package atomicfile

import (
	"errors"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

func siblings(t *testing.T, fsys billy.Filesystem, dir string) []string {
	t.Helper()
	entries, err := fsys.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// A directory lands whole: filled beside its destination and moved
// into place, an empty destination replaced, no sibling left behind;
// a failed fill, a non-empty or non-directory destination, or a failed
// move leaves the destination as found and no sibling.
func TestWriteDir(t *testing.T) {
	write := func(fsys billy.Filesystem, name string) error {
		return WriteDir(fsys, name, ".tmp-", func(dir string) error {
			return util.WriteFile(fsys, dir+"/x/y.proto", []byte("y"), 0o644)
		})
	}
	// A real tree: the landing is a directory rename, which the
	// in-memory filesystem does not model faithfully.
	m := osfs.New(t.TempDir())
	if err := write(m, "parent/out"); err != nil {
		t.Fatal(err)
	}
	if b, err := util.ReadFile(m, "parent/out/x/y.proto"); err != nil || string(b) != "y" {
		t.Fatalf("landed %q %v", b, err)
	}
	if got := siblings(t, m, "parent"); len(got) != 1 || got[0] != "out" {
		t.Fatalf("parent holds %v", got)
	}
	// An empty destination is replaced.
	if err := m.MkdirAll("parent/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := write(m, "parent/empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat("parent/empty/x/y.proto"); err != nil {
		t.Fatalf("not replaced: %v", err)
	}
	// A failed fill leaves nothing.
	boom := errors.New("boom")
	err := WriteDir(m, "parent/failed", ".tmp-", func(dir string) error {
		if err := util.WriteFile(m, dir+"/half", []byte("h"), 0o644); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("fill error: %v", err)
	}
	if got := siblings(t, m, "parent"); strings.Join(got, ",") != "empty,out" {
		t.Fatalf("after a failed fill the parent holds %v", got)
	}
	// A non-empty destination and a file are refused, untouched.
	if err := util.WriteFile(m, "parent/full/keep", []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := write(m, "parent/full"); err == nil || !strings.Contains(err.Error(), "not an empty directory") {
		t.Fatalf("non-empty: %v", err)
	}
	if err := util.WriteFile(m, "parent/file", []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := write(m, "parent/file"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a file: %v", err)
	}
	if got := siblings(t, m, "parent"); strings.Join(got, ",") != "empty,file,full,out" {
		t.Fatalf("after refusals the parent holds %v", got)
	}
	// A failed move removes the sibling and puts an empty destination
	// back.
	e := &fetchtest.ErrFS{Filesystem: memfs.New(), FailRename: true, PutFailAfter: -1}
	if err := e.MkdirAll("parent/out", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := write(e, "parent/out"); !errors.Is(err, fetchtest.ErrInjected) {
		t.Fatalf("move: %v", err)
	}
	if got := siblings(t, e, "parent"); len(got) != 1 || got[0] != "out" {
		t.Fatalf("after a failed move the parent holds %v", got)
	}
	if got := siblings(t, e, "parent/out"); len(got) != 0 {
		t.Fatalf("the empty destination holds %v", got)
	}
}
