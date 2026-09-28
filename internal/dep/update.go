package dep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"

	"github.com/greatliontech/pb/internal/module"

	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/source/fetch"
)

// PluginUpdater re-resolves a pinned oci plugin reference and rewrites
// its pin, returning the pin as it was and as it is; oci.Acquirer
// is the one implementation.
type PluginUpdater interface {
	UpdatePlugin(ctx context.Context, ref string) (before, after lockfile.PluginPin, err error)
}

// Update moves requirements to the highest tagged release discovered
// for each module (REQ-dep-update): with arguments, exactly the named
// modules — failing on a name no workspace module requires, on a name
// with no discoverable release, and on a name whose highest release is
// lower than a declaration; without arguments, every direct external
// requirement with a discoverable release higher than its declaration,
// skipping the rest. A replaced path is never discovered: the
// workspace consults the replacement, never the replaced path's origin
// — the sweep leaves its declaration and reports it as replaced, and
// naming it fails. An argument naming an oci plugin reference the
// root's generation configuration declares updates that plugin's pin
// through plugins instead — the reference resolved anew, its evidence
// fetched anew — and fails with no updater; without arguments plugins
// are left as pinned. Rewritten declarations are emitted canonically;
// rewrites are per-file atomic, not transactional — a failed run may
// leave some declaring files updated, and rerunning converges.
func Update(ctx context.Context, s *Session, out io.Writer, plugins PluginUpdater, targets ...string) error {
	// Every target is placed before any is moved, so a name that is
	// nothing fails the run whole: a declared oci plugin reference
	// names the plugin, anything else a module. The configuration is
	// consulted only for named targets; the unnamed sweep leaves
	// plugins as pinned.
	var pluginTargets, moduleTargets []string
	if len(targets) > 0 {
		pluginRefs, err := declaredPlugins(s)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, target := range targets {
			if seen[target] {
				continue
			}
			seen[target] = true
			if pluginRefs[target] {
				pluginTargets = append(pluginTargets, target)
			} else {
				moduleTargets = append(moduleTargets, target)
			}
		}
		if len(pluginTargets) > 0 {
			if plugins == nil {
				return fmt.Errorf("dep update: %s is a plugin, and no plugin updater is wired", pluginTargets[0])
			}
			if !s.Client.Policy.Execution.SchemeAllowed(plugin.SchemeOCI) {
				return fmt.Errorf("dep update: the trust policy does not permit oci-scheme plugins (plugin %s)", pluginTargets[0])
			}
		}
	}

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
		moduleTargets = slices.Sorted(func(yield func(string) bool) {
			for p := range declared {
				if !yield(p) {
					return
				}
			}
		})
	}
	for _, target := range moduleTargets {
		if _, ok := declared[target]; !ok && named {
			return fmt.Errorf("dep update: no workspace module requires %s", target)
		}
		// A replaced path is placed as nothing to move: the workspace
		// file's fact, refused before any argument moves.
		if src, srcV := s.Root.Source(target, version.Version{}); named && src != target {
			return fmt.Errorf("dep update: %s is replaced by %s@%s in the workspace file; its origin is never consulted, and the replacement's version is the workspace file's to move", target, src, srcV)
		}
	}
	// A moved plugin pin is durable before it is reported, whatever
	// the module arm does after.
	for _, target := range pluginTargets {
		before, after, err := plugins.UpdatePlugin(ctx, target)
		if err != nil {
			return fmt.Errorf("dep update: %w", err)
		}
		if err := s.SaveLock(); err != nil {
			return err
		}
		fmt.Fprintf(out, "plugin %s: %s -> %s, provenance %s -> %s\n", target, before.Digest, after.Digest, provenanceSpelling(before.Provenance), provenanceSpelling(after.Provenance))
	}

	changed := map[int]bool{}
	for _, target := range moduleTargets {
		declarers, ok := declared[target]
		if !ok {
			continue
		}
		if src, srcV := s.Root.Source(target, version.Version{}); src != target {
			fmt.Fprintf(out, "%s: replaced by %s@%s, declaration left\n", target, src, srcV)
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
		if err := writeFile(s.WS, path.Join(s.Root.Dir, m.Dir, module.ModuleFileName), b); err != nil {
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
		b, ok, err := s.Client.Cache.Get(pin.Path, v, fetch.KindZip)
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
			mb, has, err := archive.ZipModuleFile(bytes.NewReader(b), int64(len(b)))
			if err != nil || !has {
				mismatches = append(mismatches, fmt.Sprintf("%s@%s: pinned module file missing from cached archive", pin.Path, pin.Version))
				continue
			}
			if got := fetch.ModfileHash(mb); got != pin.Modfile {
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

// declaredPlugins is the set of oci plugin references the root's
// generation configuration declares, empty with no configuration.
func declaredPlugins(s *Session) (map[string]bool, error) {
	gf, err := s.GenFile()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("dep update: %w", err)
	}
	refs := map[string]bool{}
	for _, p := range gf.Plugins {
		if p.Scheme == plugin.SchemeOCI {
			refs[p.Ref] = true
		}
	}
	return refs, nil
}

// provenanceSpelling is a lockfile record as the update reports one:
// none, or the evidence type and what vouched — an identity's SAN and
// issuer, or a pinned key's kind and fingerprint
// (REQ-lock-pinned-key-record). It is total over every record the
// lockfile defines, whichever entries a caller reports.
func provenanceSpelling(p lockfile.Provenance) string {
	if p == (lockfile.Provenance{}) {
		return "none"
	}
	if lockfile.NamesKey(p) {
		return p.Type + " " + p.KeyKind + " " + p.KeyFingerprint
	}
	return p.Type + " " + p.SAN + " by " + p.Issuer
}
