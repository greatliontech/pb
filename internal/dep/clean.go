package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// Stores names what `pb clean` empties (REQ-dep-clean): the module
// cache's filesystem and the `vcs` store beneath it, and the plugin
// store's emptying — the store and its kept evidence, through the
// store's own removal and collection — which reports the images a
// live run's hold or a live mount kept, each by the platform manifest
// the store materialized.
type Stores struct {
	ModuleCache billy.Filesystem
	VCS         billy.Filesystem
	Plugins     func(ctx context.Context) (kept []string, err error)
	// Sources is the dependency source store the language server fills
	// (lsp.md REQ-lsp-dependency-files): a directory per module copy,
	// `<escaped path>@<escaped version>`, and one per toolchain's
	// well-known set, `well-known@<digest>`.
	Sources billy.Filesystem
}

// Clean empties the selected stores and reports each one emptied and
// every image kept (REQ-dep-clean): the module cache's
// version-addressed entries removed outright — the cache carries no
// authority, so a reader finding an entry gone refetches
// (REQ-dep-cache-transparent) — and each origin under vcs emptied
// under its own lock; the plugin store through its own discipline.
// Neither flag empties both.
func Clean(ctx context.Context, s Stores, out io.Writer, modules, plugins, sources bool) error {
	if !modules && !plugins && !sources {
		modules, plugins, sources = true, true, true
	}
	if modules {
		if err := emptyModuleCache(ctx, s); err != nil {
			return fmt.Errorf("clean: %w", err)
		}
		fmt.Fprintln(out, "modules: emptied")
	}
	if plugins {
		kept, err := s.Plugins(ctx)
		if err != nil {
			return fmt.Errorf("clean: %w", err)
		}
		fmt.Fprintln(out, "plugins: emptied")
		for _, digest := range kept {
			fmt.Fprintf(out, "plugins: platform manifest %s kept by a live run or mount\n", digest)
		}
	}
	if sources {
		if err := emptySources(s.Sources); err != nil {
			return fmt.Errorf("clean: %w", err)
		}
		fmt.Fprintln(out, "sources: emptied")
	}
	return nil
}

// emptySources removes what the source store's layout recognizes and
// nothing else: every copy — a module's at `<escaped path>@<escaped
// version>`, the path's segments directories as the module cache
// lays them, or a well-known set's at `well-known@<digest>` — the
// directories the emptying left empty with them, and every temporary
// an atomic write left beside a copy's file (SourcesTempPrefix). A
// store absent is empty already.
func emptySources(fsys billy.Filesystem) error {
	if _, err := fsys.Stat("."); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_, err := emptySourcesDir(fsys, ".", "")
	return err
}

// emptySourcesDir empties one directory of the store and reports
// whether it removed anything, so a parent left empty by the
// emptying goes with it; prefix is the module path spelled so far by
// the directories above.
func emptySourcesDir(fsys billy.Filesystem, dir, prefix string) (bool, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("sources: %s: %w", dir, err)
	}
	removed := false
	for _, e := range entries {
		name := e.Name()
		at := filepath.Join(dir, name)
		switch {
		case !e.IsDir():
			if strings.HasPrefix(name, SourcesTempPrefix) {
				if err := fsys.Remove(at); err != nil {
					return false, fmt.Errorf("sources: %s: %w", at, err)
				}
				removed = true
			}
		case SourcesCopyName(path.Join(prefix, name)):
			if err := util.RemoveAll(fsys, at); err != nil {
				return false, fmt.Errorf("sources: %s: %w", at, err)
			}
			removed = true
		default:
			sub, err := emptySourcesDir(fsys, at, path.Join(prefix, name))
			if err != nil {
				return false, err
			}
			if sub {
				removed = true
				if left, err := fsys.ReadDir(at); err == nil && len(left) == 0 {
					if err := fsys.Remove(at); err != nil {
						return false, fmt.Errorf("sources: %s: %w", at, err)
					}
				}
			}
		}
	}
	return removed, nil
}

// SourcesTempPrefix names the temporaries the source store's atomic
// writes leave on interruption, which the emptying removes.
const SourcesTempPrefix = ".pb-sources-"

// SourcesCopyName reports whether name — a copy directory's path
// from the store's root — is a copy of the source store: `<escaped
// path>@<escaped version>` where the halves unescape to a module
// path and a version as pb spells them, or `well-known@<digest>`
// with a hex digest; a directory spelled otherwise is a stranger's.
func SourcesCopyName(name string) bool {
	at := strings.LastIndexByte(name, '@')
	if at <= 0 || at == len(name)-1 {
		return false
	}
	head, tail := name[:at], name[at+1:]
	if head == "well-known" {
		for _, c := range tail {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return len(tail) == 64
	}
	modPath, err := proxy.Unescape(head)
	if err != nil || module.ValidatePath(modPath) != nil {
		return false
	}
	v, err := proxy.Unescape(tail)
	if err != nil {
		return false
	}
	_, err = version.Parse(v)
	return err == nil
}

// emptyModuleCache removes what the cache's layout recognizes and
// nothing else (REQ-dep-cache-layout): every artifact under an `@v`
// directory and the temporaries atomic writes leave beside them, the
// module-path directories they emptied, and the vcs store's origins,
// each under its own lock (the module cache term, dep-verbs.md). The
// cache setting can name any directory, so an entry the layout does
// not recognize is not the cache's and stays. A cache absent is
// empty already.
func emptyModuleCache(ctx context.Context, s Stores) error {
	entries, err := s.ModuleCache.ReadDir(".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("module cache: %w", err)
	}
	for _, e := range entries {
		if e.Name() == direct.StoreDir || !e.IsDir() {
			continue
		}
		if _, err := removeModuleTree(s.ModuleCache, e.Name(), e.Name()); err != nil {
			return fmt.Errorf("module cache: %w", err)
		}
	}
	if s.VCS == nil {
		return nil
	}
	if err := direct.Empty(ctx, s.VCS); err != nil {
		return fmt.Errorf("module cache: %w", err)
	}
	return nil
}

// removeModuleTree walks a directory of the cache, escaped the
// escaped module path its cache-relative path spells so far: an `@v`
// directory beneath a path that unescapes to a module path loses its
// artifacts and temporaries, any other directory is walked, and a
// directory is removed only when the walk removed something beneath
// it and left it empty — one that was empty already, or holds
// anything the layout does not recognize, is not the cache's and
// stays. It reports whether the walk removed anything.
func removeModuleTree(fsys billy.Filesystem, dir, escaped string) (bool, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return false, err
	}
	removed := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := fsys.Join(dir, e.Name())
		var did bool
		if e.Name() == fetch.VersionDir && fetch.IsModuleDir(escaped) {
			did, err = removeArtifacts(fsys, p)
		} else {
			did, err = removeModuleTree(fsys, p, escaped+"/"+e.Name())
		}
		if err != nil {
			return false, err
		}
		removed = removed || did
	}
	if !removed {
		return false, nil
	}
	return true, removeIfEmpty(fsys, dir)
}

// removeArtifacts empties an `@v` directory of what the layout
// recognizes — an artifact of one of the cache's kinds, a temporary
// of an atomic write — and removes it when that left it empty,
// reporting whether it removed anything.
func removeArtifacts(fsys billy.Filesystem, dir string) (bool, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return false, err
	}
	removed := false
	for _, e := range entries {
		if e.IsDir() || !fetch.IsArtifactName(e.Name()) {
			continue
		}
		if err := fsys.Remove(fsys.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		removed = true
	}
	if !removed {
		return false, nil
	}
	return true, removeIfEmpty(fsys, dir)
}

// removeIfEmpty removes a directory when it is empty.
func removeIfEmpty(fsys billy.Filesystem, dir string) error {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return nil
	}
	if err := fsys.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
