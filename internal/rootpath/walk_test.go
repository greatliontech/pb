package rootpath

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
)

// Walk visits the tree under the root as given, each path slash-spelled
// and rooted at it, files and directories alike, the host's separator
// never reaching the caller.
func TestWalkSlashSpelled(t *testing.T) {
	fsys := memfs.New()
	for _, f := range []string{"a/b/c.proto", "a/d.txt", "e.proto"} {
		if err := util.WriteFile(fsys, f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	err := Walk(fsys, "a", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(p, "\\") {
			t.Errorf("a walked path with the host's separator: %q", p)
		}
		seen = append(seen, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(seen)
	if got := strings.Join(seen, " "); got != "a a/b a/b/c.proto a/d.txt" {
		t.Fatalf("walked %q", got)
	}
}
