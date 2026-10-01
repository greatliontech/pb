//go:build !unix

package dep

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6"
)

// sameDirectory reports whether the directories at a and b, paths
// within the working tree, name one directory: the host's own
// identity comparison over the paths as the host spells them — on
// windows the volume and file index, which no info the tree hands
// out carries — read through the tree's root, so a case-folded or
// otherwise re-spelled path to a module's directory is that
// directory (platforms.md REQ-plat-files). A tree with no root on
// the host, the in-memory one, names nothing the same.
func sameDirectory(ws billy.Filesystem, a, b string, _ fs.FileInfo) (bool, error) {
	rooted, ok := ws.(interface{ Root() string })
	if !ok {
		return false, nil
	}
	fa, err := os.Stat(filepath.Join(rooted.Root(), filepath.FromSlash(a)))
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(filepath.Join(rooted.Root(), filepath.FromSlash(b)))
	if err != nil {
		return false, nil
	}
	return os.SameFile(fa, fb), nil
}
