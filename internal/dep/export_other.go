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
// directory (platforms.md REQ-plat-files). Infos carrying no host
// stat, the in-memory tree's, are never the same — its root spelled
// as the host's own would stat the host's directories otherwise;
// b absent is not an error, merely not the same.
func sameDirectory(ws billy.Filesystem, a, b string, fi fs.FileInfo) (bool, error) {
	if fi.Sys() == nil {
		return false, nil
	}
	fa, err := os.Stat(filepath.Join(ws.Root(), filepath.FromSlash(a)))
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(filepath.Join(ws.Root(), filepath.FromSlash(b)))
	if err != nil {
		return false, nil
	}
	return os.SameFile(fa, fb), nil
}
