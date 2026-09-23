//go:build unix

package dep

import (
	"io/fs"
	"syscall"
)

// sameDirectory reports whether two directory infos name one
// directory: the same device and inode, read from the stat the
// working tree's infos carry — the tree's own info type, which the
// standard identity comparison does not recognise. Infos carrying no
// stat, the in-memory tree's, are never the same.
func sameDirectory(a, b fs.FileInfo) bool {
	sa, oka := a.Sys().(*syscall.Stat_t)
	sb, okb := b.Sys().(*syscall.Stat_t)
	return oka && okb && sa.Dev == sb.Dev && sa.Ino == sb.Ino
}
