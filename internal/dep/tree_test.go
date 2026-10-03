package dep

import (
	"testing"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// A tree path falls in the module whose directory is its longest
// prefix, unless a directory between holds a module file — a nested
// module's — or is a symbolic link, through which no file is a
// module's, as the file sets' walk never descends one
// (module-archive.md REQ-archive-links-carried).
func TestFileOfNestingAndLinks(t *testing.T) {
	ws := memfs.New()
	for p, body := range map[string]string{
		"ws/pb.work":           "use:\n  - a\n",
		"ws/a/pb.yaml":         "module: example.com/a\n",
		"ws/a/x.proto":         "syntax = \"proto3\";\n",
		"ws/a/nested/pb.yaml":  "module: example.com/nested\n",
		"ws/a/nested/y.proto":  "syntax = \"proto3\";\n",
		"ws/elsewhere/z.proto": "syntax = \"proto3\";\n",
	} {
		if err := util.WriteFile(ws, p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ws.Symlink("../elsewhere", "ws/a/link"); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.LoadFor(iofs.New(ws), "ws")
	if err != nil {
		t.Fatal(err)
	}
	// The file sets' walk agrees: the member's own file alone, the
	// nested module's and the linked directory's never read.
	files, _, err := modfiles.WorkspaceFiles(iofs.New(ws), "ws/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files["x.proto"] == nil {
		t.Fatalf("the walk read %v, want x.proto alone", files)
	}
	tree := Tree{WS: ws, Root: root}
	mods := modfiles.Members(root)
	for rel, want := range map[string]bool{
		"a/x.proto":         true,
		"a/nested/y.proto":  false,
		"a/link/z.proto":    false,
		"elsewhere/z.proto": false,
	} {
		i, file, ok := tree.FileOf(mods, rel)
		if ok != want || (ok && (i != 0 || file != "x.proto")) {
			t.Errorf("%s: %d %q %v, want %v", rel, i, file, ok, want)
		}
	}
}
