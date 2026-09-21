// Package gitdir finds the git repository a directory lies in: the
// directory or its nearest ancestor holding a .git entry, the search
// honoring GIT_CEILING_DIRECTORIES as git does (check-rules.md
// REQ-break-base-materialized). Two specs give a verb the search —
// the breaking check materializing a base from the repository's
// history (check-rules.md), and the migration reading the repository's
// origin (migrate.md) — and one rule serves both, so the two can never
// disagree on which repository a directory belongs to.
package gitdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
)

// RepoOf opens the git repository a directory — an operating-system
// path, made absolute — lies in, the directory or its nearest ancestor
// holding a .git entry, and yields the directory's slash-separated
// path relative to the repository's root, empty at the root; an error
// where none does. The search never enters a directory
// GIT_CEILING_DIRECTORIES names (check-rules.md
// REQ-break-base-materialized).
func RepoOf(dir string) (*git.Repository, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	root, ok := repositoryRoot(abs, ceilings(os.Getenv("GIT_CEILING_DIRECTORIES")), holdsGit)
	if !ok {
		return nil, "", fmt.Errorf("%s lies in no git repository", abs)
	}
	repo, err := git.PlainOpen(root)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", root, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return nil, "", err
	}
	if rel == "." {
		rel = ""
	}
	return repo, filepath.ToSlash(rel), nil
}

// holdsGit reports whether a directory holds a .git entry, a
// directory or git's file form alike.
func holdsGit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, git.GitDirName))
	return err == nil
}

// A ceiling is a directory the repository search never enters: one
// GIT_CEILING_DIRECTORIES names by identity — the directory the
// entry resolves to, however either is spelled — or, after an empty
// entry, by its spelling alone.
type ceiling struct {
	path string
	// The directory the entry resolves to, for a ceiling by identity;
	// nil for one by spelling.
	dir os.FileInfo
}

// ceilings reads GIT_CEILING_DIRECTORIES as git reads it: absolute
// entries alone, a relative entry and the filesystem's root dropped,
// each entry as written but for a trailing separator, naming the
// directory it resolves to — one that resolves to none names
// nothing — until an empty entry, after which an entry names its
// spelling alone.
func ceilings(list string) []ceiling {
	var out []ceiling
	identity := true
	for _, c := range filepath.SplitList(list) {
		if c == "" {
			identity = false
			continue
		}
		if !filepath.IsAbs(c) {
			continue
		}
		c = strings.TrimRightFunc(c, func(r rune) bool { return r < 0x80 && os.IsPathSeparator(uint8(r)) })
		if c == filepath.VolumeName(c) {
			continue
		}
		if !identity {
			out = append(out, ceiling{path: c})
			continue
		}
		if fi, err := os.Stat(c); err == nil {
			out = append(out, ceiling{path: c, dir: fi})
		}
	}
	return out
}

// is reports whether a directory is the ceiling: its spelling, or,
// for a ceiling by identity, the same directory — resolved by the
// operating system in one step, no path walked by hand.
func (c ceiling) is(dir string) bool {
	if c.path == dir {
		return true
	}
	if c.dir == nil {
		return false
	}
	got, err := os.Stat(dir)
	return err == nil && os.SameFile(c.dir, got)
}

// repositoryRoot walks up from an absolute directory to the first
// that holds a .git entry, the filesystem's root searched last, and
// never enters a ceiling.
func repositoryRoot(abs string, stops []ceiling, holdsGit func(string) bool) (string, bool) {
	for dir := abs; ; {
		if holdsGit(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		for _, c := range stops {
			if c.is(parent) {
				return "", false
			}
		}
		dir = parent
	}
}
