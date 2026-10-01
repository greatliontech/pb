package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/format"
)

// ErrUnformatted is the format verb's failing status under
// --exit-code: some own file was not in canonical form, which the
// output has already said (format.md REQ-format-verb).
var ErrUnformatted = errors.New("files not formatted")

// FormatOptions are the format verb's flags.
type FormatOptions struct {
	Diff     bool // print a unified diff per unformatted file instead of its path
	Write    bool // rewrite each unformatted file in place
	ExitCode bool // fail where any file was unformatted
}

// Format reads every own file of the workspace — each workspace
// module's protobuf sources, a copy of a well-known file among them —
// computes its canonical form, and acts on the unformatted ones as
// the options say: their paths, or their diffs, on out in path
// order, each rewritten in place under Write; it fails before writing
// anything where a file does not parse or where a file to rewrite is
// a symbolic link (format.md REQ-format-verb).
func Format(ctx context.Context, s *Session, opts FormatOptions, out io.Writer) error {
	_, mods, err := s.Modules(ctx)
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	type own struct {
		path           string // from the workspace root
		src, canonical []byte
	}
	var files []own
	for _, m := range mods {
		if !m.Local {
			continue
		}
		for _, p := range slices.Sorted(maps.Keys(m.Files)) {
			if !strings.HasSuffix(p, ".proto") {
				continue
			}
			src := m.Files[p]
			rel := path.Join(m.Dir, p)
			canonical, err := format.Format(rel, src)
			if err != nil {
				return fmt.Errorf("format: %w", err)
			}
			if string(canonical) != string(src) {
				files = append(files, own{path: rel, src: src, canonical: canonical})
			}
		}
	}
	sort.Slice(files, func(a, b int) bool { return files[a].path < files[b].path })
	if opts.Write {
		// A symbolic link is no file the verb replaces: refused before
		// anything is written.
		for _, f := range files {
			info, err := s.WS.Lstat(path.Join(s.Root.Dir, f.path))
			if err != nil {
				return fmt.Errorf("format: %w", err)
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("format: %s is a symbolic link, which the verb does not rewrite", f.path)
			}
		}
	}
	for _, f := range files {
		if opts.Diff {
			if _, err := out.Write(format.Unified(f.path, f.src, f.canonical)); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintln(out, f.path); err != nil {
			return err
		}
		if opts.Write {
			// Replaced atomically, a crash leaving the file as it was,
			// its mode kept.
			at := path.Join(s.Root.Dir, f.path)
			info, err := s.WS.Stat(at)
			if err != nil {
				return fmt.Errorf("format: %w", err)
			}
			if err := atomicfile.Write(s.WS, at, ".pb-format-", info.Mode().Perm(), f.canonical); err != nil {
				return fmt.Errorf("format: %s: %w", f.path, err)
			}
		}
	}
	if opts.ExitCode && len(files) > 0 {
		return ErrUnformatted
	}
	return nil
}
