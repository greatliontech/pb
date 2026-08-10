package dep

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"slices"

	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfetch"
	"github.com/greatliontech/pb/internal/modfile"
	"github.com/greatliontech/pb/internal/version"
)

// Update moves requirements to the highest tagged release discovered
// for each module (REQ-dep-update): with arguments, exactly the named
// modules — failing on a name no workspace module requires, on a name
// with no discoverable release, and on a name whose highest release is
// lower than a declaration; without arguments, every direct external
// requirement with a discoverable release higher than its declaration,
// skipping the rest. Rewritten declarations are emitted canonically;
// rewrites are per-file atomic, not transactional — a failed run may
// leave some declaring files updated, and rerunning converges.
func Update(ctx context.Context, s *Session, out io.Writer, targets ...string) error {
	declared := map[string][]int{} // path -> indexes of declaring modules
	for i, m := range s.Root.Modules {
		for p := range m.File.Deps {
			if _, local := s.Root.IsLocal(p); local {
				continue
			}
			declared[p] = append(declared[p], i)
		}
	}

	named := len(targets) > 0
	if !named {
		targets = slices.Sorted(func(yield func(string) bool) {
			for p := range declared {
				if !yield(p) {
					return
				}
			}
		})
	}

	changed := map[int]bool{}
	for _, target := range targets {
		declarers, ok := declared[target]
		if !ok {
			if named {
				return fmt.Errorf("dep update: no workspace module requires %s", target)
			}
			continue
		}
		versions, err := s.Client.Versions(ctx, target)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			if named {
				return fmt.Errorf("dep update: %s has no discoverable release", target)
			}
			continue
		}
		highest := versions[len(versions)-1]
		for _, i := range declarers {
			m := s.Root.Modules[i]
			cur, err := version.Parse(m.File.Deps[target])
			if err != nil {
				return fmt.Errorf("%s: requirement %s@%s: %w", m.File.Module, target, m.File.Deps[target], err)
			}
			switch c := version.Compare(highest, cur); {
			case c > 0:
				m.File.Deps[target] = highest.String()
				changed[i] = true
				fmt.Fprintf(out, "%s: %s %s -> %s\n", m.File.Module, target, cur, highest)
			case c < 0:
				if named {
					return fmt.Errorf("dep update: the highest discovered release of %s is %s, below the declared %s — the origin regressed; not updating silently", target, highest, cur)
				}
			}
		}
	}

	for i, m := range s.Root.Modules {
		if !changed[i] {
			continue
		}
		b, err := modfile.Encode(m.File)
		if err != nil {
			return err
		}
		if err := writeFile(s.WS, path.Join(s.Root.Dir, m.Dir, modfile.ModuleFileName), b); err != nil {
			return err
		}
	}

	// Resolve over the updated declarations so new pins are recorded
	// (REQ-lock-first-use; any rewrite of an existing pin is the
	// explicit update REQ-lock-no-silent-downgrade sanctions).
	if _, _, err := s.Driver.BuildList(ctx); err != nil {
		return err
	}
	return s.SaveLock()
}

// Verify recomputes, for every pinned pair whose artifacts are present
// in the module cache, the module digest and module-file hash against
// the pin (REQ-dep-verify), reporting every mismatch and failing when
// any exists. Pairs with no cached artifacts are outside its scope.
func Verify(ctx context.Context, s *Session, out io.Writer) error {
	pins := slices.Clone(s.Lock.Modules)
	slices.SortFunc(pins, func(a, b lockfile.ModulePin) int {
		if c := bytes.Compare([]byte(a.Path), []byte(b.Path)); c != 0 {
			return c
		}
		return bytes.Compare([]byte(a.Version), []byte(b.Version))
	})
	var mismatches []string
	verified := 0
	for _, pin := range pins {
		v, err := version.Parse(pin.Version)
		if err != nil {
			return fmt.Errorf("pin %s@%s: %w", pin.Path, pin.Version, err)
		}
		b, ok, err := s.Client.Cache.Get(pin.Path, v, modfetch.KindZip)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		digest, _, err := archive.DigestZip(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s@%s: cached archive unreadable: %v", pin.Path, pin.Version, err))
			continue
		}
		if digest != pin.Digest {
			mismatches = append(mismatches, fmt.Sprintf("%s@%s: digest %s, pin records %s", pin.Path, pin.Version, digest, pin.Digest))
			continue
		}
		if pin.Modfile != "" {
			mb, has, err := archive.ZipFile(bytes.NewReader(b), int64(len(b)), archive.ModuleFileName)
			if err != nil || !has {
				mismatches = append(mismatches, fmt.Sprintf("%s@%s: pinned module file missing from cached archive", pin.Path, pin.Version))
				continue
			}
			if got := modfetch.ModfileHash(mb); got != pin.Modfile {
				mismatches = append(mismatches, fmt.Sprintf("%s@%s: modfile %s, pin records %s", pin.Path, pin.Version, got, pin.Modfile))
				continue
			}
		}
		verified++
	}
	for _, m := range mismatches {
		fmt.Fprintln(out, m)
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("dep verify: %d cached artifact(s) disagree with their pins", len(mismatches))
	}
	fmt.Fprintf(out, "verified %d cached module(s)\n", verified)
	return nil
}
