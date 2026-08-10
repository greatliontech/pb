// Package atomicfile writes files atomically and whole over a billy
// filesystem: a temporary file in the target's directory, renamed into
// place, so no reader ever observes a partial file. One implementation
// serves every writer that needs the discipline — the module cache and
// the verb layer's module-file and lockfile rewrites.
package atomicfile

import (
	"github.com/go-git/go-billy/v6"
)

// Write writes data to name atomically. On error the temporary file is
// removed best-effort and the target is untouched.
func Write(fs billy.Filesystem, name, tmpPrefix string, data []byte) error {
	dir := billyDir(name)
	tmp, err := fs.TempFile(dir, tmpPrefix)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		fs.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		fs.Remove(tmpName)
		return err
	}
	if err := fs.Rename(tmpName, name); err != nil {
		fs.Remove(tmpName)
		return err
	}
	return nil
}

func billyDir(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[:i]
		}
	}
	return "."
}
