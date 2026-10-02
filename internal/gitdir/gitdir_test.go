package gitdir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// The repository search reads GIT_CEILING_DIRECTORIES as git does —
// absolute entries alone, by identity until an empty entry and by
// spelling after it — never enters a ceiling, an ancestor spelled
// through a link included, never excludes the start directory, and
// searches the filesystem's root last (check-rules.md
// REQ-break-base-materialized).
func TestRepositorySearch(t *testing.T) {
	dir := scratchtest.Dir(t)
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	// A second link into a subdirectory, so that ".." after it
	// resolves to that subdirectory's parent, not the link's.
	deep := filepath.Join(dir, "deep")
	link2 := filepath.Join(dir, "link2")
	for _, d := range []string{filepath.Join(real, "a", "b"), filepath.Join(deep, "target"), filepath.Join(deep, "other"), filepath.Join(dir, "other")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(deep, "target"), link2); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	ls := string(os.PathListSeparator)     // git reads the list as PATH is read, the platform's separator
	root := filepath.VolumeName(dir) + sep // the filesystem's root, a volume's on windows
	got := ceilings("relative" + ls + root + ls + link + sep + ls + filepath.Join(dir, "nonesuch") + ls + ls + dir + sep + "x" + sep + ".." + sep + "y" + sep + ls + root)
	if len(got) != 2 || got[0].path != link || got[0].dir == nil || got[1].path != dir+sep+"x"+sep+".."+sep+"y" || got[1].dir != nil {
		t.Fatalf("ceilings = %+v", got)
	}
	// An entry resolves as the operating system resolves it: through
	// the link first, then up.
	if c := ceilings(link2 + sep + ".." + sep + "other"); len(c) != 1 || !c[0].is(filepath.Join(deep, "other")) || c[0].is(filepath.Join(dir, "other")) {
		t.Fatalf("an entry through a link and up: %+v", c)
	}
	never := func(string) bool { return false }
	at := func(root string) func(string) bool { return func(d string) bool { return d == root } }
	viaLink := filepath.Join(link, "a", "b")
	viaReal := filepath.Join(real, "a", "b")
	if root, ok := repositoryRoot(viaLink, nil, never); ok || root != "" {
		t.Fatalf("no .git anywhere: %q %v", root, ok)
	}
	if root, ok := repositoryRoot(viaLink, nil, at(filepath.Join(link, "a"))); !ok || root != filepath.Join(link, "a") {
		t.Fatalf("the nearest ancestor: %q %v", root, ok)
	}
	if root, ok := repositoryRoot(viaLink, nil, at(string(filepath.Separator))); !ok || root != string(filepath.Separator) {
		t.Fatalf("the filesystem's root searched last: %q %v", root, ok)
	}
	// By identity, the ceiling stops a walk spelled either way; by
	// spelling, only the walk spelled as the entry is.
	if _, ok := repositoryRoot(viaLink, ceilings(real), at(dir)); ok {
		t.Fatal("entered a ceiling named by identity, reached through a link")
	}
	if _, ok := repositoryRoot(viaReal, ceilings(link), at(dir)); ok {
		t.Fatal("entered a ceiling named by identity through a link, reached directly")
	}
	if _, ok := repositoryRoot(viaLink, ceilings(":"+link), at(dir)); ok {
		t.Fatal("entered a ceiling named by spelling, reached as spelled")
	}
	if root, ok := repositoryRoot(viaReal, ceilings(":"+link), at(dir)); !ok || root != dir {
		t.Fatalf("a ceiling named by spelling stopped a walk spelled otherwise: %q %v", root, ok)
	}
	// A ceiling not on the path stops nothing; the start directory
	// itself is never excluded, even when named.
	if root, ok := repositoryRoot(viaLink, ceilings(filepath.Join(dir, "elsewhere")), at(dir)); !ok || root != dir {
		t.Fatalf("an unrelated ceiling: %q %v", root, ok)
	}
	if root, ok := repositoryRoot(viaLink, ceilings(viaLink), at(dir)); !ok || root != dir {
		t.Fatalf("the start directory as a ceiling: %q %v", root, ok)
	}
	// A ceiling that does not exist is no ceiling.
	if root, ok := repositoryRoot(viaLink, ceilings(filepath.Join(dir, "nonesuch")), at(dir)); !ok || root != dir {
		t.Fatalf("an absent ceiling: %q %v", root, ok)
	}
}
