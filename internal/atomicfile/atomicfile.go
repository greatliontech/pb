// Package atomicfile writes files, and directories, atomically and
// whole over a billy filesystem: a temporary file or directory beside
// the target, renamed into place, so no reader ever observes a partial
// file or tree. One implementation serves every writer that needs the
// discipline — the module cache, the verb layer's module-file and
// lockfile rewrites, an export's tree.
package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
)

// Write writes data to name atomically, the file landing at perm less
// the process's umask — created so, as any file is, never changed to
// — a file already there replaced. On error the temporary file is
// removed best-effort and the target is untouched.
func Write(fsys billy.Filesystem, name, tmpPrefix string, perm fs.FileMode, data []byte) error {
	tmpName, err := sibling(name, tmpPrefix)
	if err != nil {
		return err
	}
	// A temporary file made by the filesystem's own temp-file call
	// would be owner-only whatever perm says; created here, it carries
	// perm as the creation default, the umask applied by the system.
	tmp, err := fsys.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		fsys.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		fsys.Remove(tmpName)
		return err
	}
	if err := fsys.Rename(tmpName, name); err != nil {
		fsys.Remove(tmpName)
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

// WriteDir fills a directory whole: fill writes into a temporary
// sibling of name, which is moved into place last, so no reader
// observes a partial tree. name's parent exists, and name is absent or
// an empty directory: the move refuses to replace an existing name (a
// bound tree renames with no replacement), so an empty directory is
// removed just before the move and put back, at its mode, should the
// move fail, a restore that fails reported with the move's error. An
// error from fill or from the move removes the sibling and leaves
// name as found; an interruption leaves at most the sibling, its name
// beginning with tmpPrefix, and, between the removal and the move,
// the empty directory absent.
func WriteDir(fsys billy.Filesystem, name, tmpPrefix string, fill func(dir string) error) error {
	existing, err := emptyDestination(fsys, name)
	if err != nil {
		return err
	}
	tmp, err := sibling(name, tmpPrefix)
	if err != nil {
		return err
	}
	if err := fsys.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := fill(tmp); err != nil {
		util.RemoveAll(fsys, tmp)
		return err
	}
	if existing != nil {
		if err := fsys.Remove(name); err != nil {
			util.RemoveAll(fsys, tmp)
			return err
		}
	}
	if err := fsys.Rename(tmp, name); err != nil {
		util.RemoveAll(fsys, tmp)
		if existing != nil {
			if rerr := restoreDir(fsys, name, existing.Mode().Perm()); rerr != nil {
				return fmt.Errorf("%w; and the empty directory removed for the move could not be put back: %w", err, rerr)
			}
		}
		return err
	}
	return nil
}

// restoreDir recreates an empty directory at name with exactly perm,
// the creation's umask undone where the tree can change modes.
func restoreDir(fsys billy.Filesystem, name string, perm fs.FileMode) error {
	if err := fsys.MkdirAll(name, perm); err != nil {
		return err
	}
	if c, ok := fsys.(billy.Change); ok {
		return c.Chmod(name, perm)
	}
	return nil
}

// emptyDestination refuses a name that is neither absent nor an empty
// directory, returning the empty directory's info when there is one.
func emptyDestination(fsys billy.Filesystem, name string) (fs.FileInfo, error) {
	fi, err := fsys.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !fi.IsDir():
		return nil, fmt.Errorf("%s exists and is not a directory", name)
	}
	entries, err := fsys.ReadDir(name)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf("%s is not an empty directory", name)
	}
	return fi, nil
}

// sibling names a temporary beside name: its directory, the prefix
// and a random suffix no other writer picks.
func sibling(name, tmpPrefix string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return path.Join(billyDir(name), tmpPrefix+hex.EncodeToString(suffix[:])), nil
}
