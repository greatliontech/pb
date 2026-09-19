package plugrun

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeTar streams dir as a tar archive: directories, regular files
// and symlinks in the walk's lexical order, modes kept, owner root,
// no timestamps — the same export always yields the same bytes, a
// reproducible byte path (the daemon names each import as it likes).
// Anything else in the
// tree (a device, a socket) is refused: an image export holds none.
func writeTar(w io.Writer, dir string) error {
	var paths []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), Format: tar.FormatPAX}
		switch {
		case fi.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Name = name + "/"
		case fi.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Size = fi.Size()
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = target
		default:
			return fmt.Errorf("plugrun: %s in the export is neither a directory, a file nor a symlink", name)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("plugrun: reading %s: %w", strings.TrimSuffix(name, "/"), err)
			}
		}
	}
	return tw.Close()
}
