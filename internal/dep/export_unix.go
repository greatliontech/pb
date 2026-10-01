//go:build unix

package dep

import (
	"io/fs"
	"syscall"

	"github.com/go-git/go-billy/v6"
)

// sameDirectory reports whether the directory at a, whose info is fi,
// and the directory at b name one directory: the same device and
// inode, read from the stat the working tree's infos carry — the
// tree's own info type, which the standard identity comparison does
// not recognise. Infos carrying no stat, the in-memory tree's, are
// never the same; b absent is not an error, merely not the same.
func sameDirectory(ws billy.Filesystem, a, b string, fi fs.FileInfo) (bool, error) {
	mfi, err := ws.Stat(b)
	if err != nil {
		return false, nil
	}
	sa, oka := fi.Sys().(*syscall.Stat_t)
	sb, okb := mfi.Sys().(*syscall.Stat_t)
	return oka && okb && sa.Dev == sb.Dev && sa.Ino == sb.Ino, nil
}
