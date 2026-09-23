package atomicfile

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

// A file lands whole at the mode asked for, a file already there
// replaced, no temporary left; a failed move leaves the target as it
// was and no temporary.
func TestWrite(t *testing.T) {
	m := memfs.New()
	if err := Write(m, "d/f", ".tmp-", 0o644, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if b, err := util.ReadFile(m, "d/f"); err != nil || string(b) != "one" {
		t.Fatalf("landed %q %v", b, err)
	}
	if fi, err := m.Stat("d/f"); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	if err := Write(m, "d/f", ".tmp-", 0o644, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if b, _ := util.ReadFile(m, "d/f"); string(b) != "two" {
		t.Fatalf("not replaced: %q", b)
	}
	if got := siblings(t, m, "d"); len(got) != 1 || got[0] != "f" {
		t.Fatalf("d holds %v", got)
	}
	e := &fetchtest.ErrFS{Filesystem: m, FailRename: true, PutFailAfter: -1}
	if err := Write(e, "d/f", ".tmp-", 0o644, []byte("three")); !errors.Is(err, fetchtest.ErrInjected) {
		t.Fatalf("move: %v", err)
	}
	if b, _ := util.ReadFile(m, "d/f"); string(b) != "two" {
		t.Fatalf("a failed move changed the target: %q", b)
	}
	if got := siblings(t, m, "d"); len(got) != 1 {
		t.Fatalf("after a failed move d holds %v", got)
	}
	// A failed creation, write or close leaves nothing: no target, no
	// temporary.
	for name, e := range map[string]*fetchtest.ErrFS{
		"creation": {Filesystem: m, FailCreate: true, PutFailAfter: -1},
		"write":    {Filesystem: m, FailWrite: true, PutFailAfter: -1},
		"close":    {Filesystem: m, FailClose: true, PutFailAfter: -1},
	} {
		if err := Write(e, "d/g", ".tmp-", 0o644, []byte("x")); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := m.Stat("d/g"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a failed %s left the target: %v", name, err)
		}
		if got := siblings(t, m, "d"); len(got) != 1 || got[0] != "f" {
			t.Fatalf("after a failed %s d holds %v", name, got)
		}
	}
}
