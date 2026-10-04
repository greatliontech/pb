package dep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/greatliontech/pb/internal/module/mvs"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/check/lintfile"
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
	var pairTargets []string
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
			switch {
			case pluginRefs[target]:
				pluginTargets = append(pluginTargets, target)
			case strings.Contains(target, "@"):
				pairTargets = append(pairTargets, target)
			default:
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

	imports, err := s.importsToMove()
	if err != nil {
		return err
	}
	// The lint file's fetched imports move as declarations do
	// (REQ-dep-ruleset-declarations); a working-tree import has
	// nothing to move, a replaced one is left as a declaration is,
	// and one written without a version is moved to the highest
	// release, a requirement with no release at all.
	imported := map[string][]int{} // path -> indexes of importing entries
	workingTree := map[string]bool{}
	for i, ri := range imports {
		switch {
		case ri.Local && s.Root.Replaced(ri.imp.Path):
			imported[ri.imp.Path] = append(imported[ri.imp.Path], i)
		case ri.Local:
			workingTree[ri.imp.Path] = true
		default:
			imported[ri.imp.Path] = append(imported[ri.imp.Path], i)
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
			for p := range imported {
				if _, twice := declared[p]; !twice && !yield(p) {
					return
				}
			}
		})
	}
	for _, target := range moduleTargets {
		if _, ok := declared[target]; !ok && named {
			if _, ok := imported[target]; !ok {
				if workingTree[target] {
					return fmt.Errorf("dep update: %s is a workspace module the lint file imports from the working tree: nothing to move", target)
				}
				return fmt.Errorf("dep update: no workspace module requires %s, and the lint file imports it not", target)
			}
		}
		// A replaced path is placed as nothing to move: the workspace
		// file's fact, refused before any argument moves.
		if named && s.Root.Replaced(target) {
			return fmt.Errorf("dep update: %s is replaced by %s in the workspace file; its origin is never consulted, and what the replacement names is the workspace file's to move", target, s.Root.Source(target, version.Version{}))
		}
	}
	// A pinned pair named as <path>@<version> is re-resolved: its pin
	// dropped and recorded anew by the first-use pipeline under the
	// trust policy of the day — the explicit update every hold names
	// (provenance.md REQ-prov-pin-held) — durable before the next.
	for _, target := range pairTargets {
		if err := reresolve(ctx, s, out, target); err != nil {
			return err
		}
		if err := s.SaveLock(); err != nil {
			return err
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

	// The module arm's report is written whole once every move is
	// placed: a failure midway reports nothing it did not do.
	var report []string
	// move judges one version against the highest release: moved when
	// higher; a highest below the current an origin that regressed,
	// refused when named and left when swept.
	move := func(cur, highest version.Version, what string) (bool, error) {
		switch c := version.Compare(highest, cur); {
		case c > 0:
			return true, nil
		case c < 0 && named:
			return false, fmt.Errorf("dep update: the highest discovered release %s is below the %s %s — the origin regressed; not updating silently", highest, what, cur)
		}
		return false, nil
	}
	changed := map[int]bool{}
	var movedImports []int
	lf, err := s.LintFile()
	if err != nil {
		return err
	}
	for _, target := range moduleTargets {
		declarers, declares := declared[target]
		importers, hasImports := imported[target]
		if !declares && !hasImports {
			continue
		}
		if s.Root.Replaced(target) {
			what := "declaration"
			if !declares {
				what = "import"
			}
			report = append(report, fmt.Sprintf("%s: replaced by %s, %s left", target, s.Root.Source(target, version.Version{}), what))
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
			// A versionless import stays versionless, which no check
			// run reads: said, never silent.
			for _, i := range importers {
				if imports[i].versionless {
					report = append(report, fmt.Sprintf("%s: %s (no version) left: no release discovered", lintfile.FileName, target))
				}
			}
			continue
		}
		highest := versions[len(versions)-1]
		// Of several imports of one path, the highest moves (or stands
		// at the release already); another that would land on the same
		// release would read one ruleset under two aliases, which the
		// lint file refuses: the sweep leaves it and says so, naming
		// the path fails before anything moves.
		slices.SortFunc(importers, func(a, b int) int { return version.Compare(imports[b].Version, imports[a].Version) })
		// The highest import ends at the release when it moves there or
		// stands there; above it (a regressed origin) it stays, and a
		// lower import moving collides with nothing.
		firstAtHighest := len(importers) > 0 && version.Compare(highest, imports[importers[0]].Version) >= 0
		for n, i := range importers {
			cur := imports[i].Version
			curText := cur.String()
			moved := true
			if imports[i].versionless {
				// No release at all: every release is above it.
				curText = "(no version)"
			} else if moved, err = move(cur, highest, "imported"); err != nil {
				return fmt.Errorf("%w (%s, imported as %s)", err, target, imports[i].imp.Alias)
			}
			if n > 0 && moved && firstAtHighest {
				first := imports[importers[0]].imp.Alias
				if named {
					return fmt.Errorf("dep update: %s is imported as %s and as %s; the highest discovered release %s would be read under both", target, first, imports[i].imp.Alias, highest)
				}
				report = append(report, fmt.Sprintf("%s: %s %s left: %s is read as %s already", lintfile.FileName, target, curText, highest, first))
				continue
			}
			if moved {
				lf.Rulesets[i].Version = highest.String()
				movedImports = append(movedImports, i)
				report = append(report, fmt.Sprintf("%s: %s %s -> %s", lintfile.FileName, target, curText, highest))
			}
		}
		for _, i := range declarers {
			m := s.Root.Modules[i]
			cur, err := version.Parse(m.File.Deps[target])
			if err != nil {
				return fmt.Errorf("%s: requirement %s@%s: %w", m.File.Module, target, m.File.Deps[target], err)
			}
			moved, err := move(cur, highest, "declared")
			if err != nil {
				return fmt.Errorf("%w (%s)", err, target)
			}
			if moved {
				m.File.Deps[target] = highest.String()
				changed[i] = true
				report = append(report, fmt.Sprintf("%s: %s %s -> %s", m.File.Module, target, cur, highest))
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
	if len(movedImports) > 0 {
		b, err := lintfile.Encode(lf)
		if err != nil {
			return err
		}
		if err := writeFile(s.WS, path.Join(s.Root.Dir, lintfile.FileName), b); err != nil {
			return err
		}
	}

	for _, line := range report {
		fmt.Fprintln(out, line)
	}

	// Resolve over the updated declarations so new pins are recorded
	// (REQ-lock-first-use; any rewrite of an existing pin is the
	// explicit update REQ-lock-no-silent-downgrade sanctions), and
	// read each moved import so its pair is pinned as a ruleset.
	_, _, err = s.Driver.BuildList(ctx)
	if err := savePins(s, err); err != nil {
		return err
	}
	for _, i := range movedImports {
		res, err := lintfile.Resolve(s.Root, lf.Rulesets[i])
		if err != nil {
			return err
		}
		_, err = s.Client.RulesetZip(ctx, res.Source.Path, res.Source.Version)
		if err := savePins(s, err); err != nil {
			return err
		}
	}
	return s.SaveLock()
}

// Verify recomputes, for every pinned pair whose artifacts are present
// in the module cache, the module digest and module-file hash against
// the pin, and holds the pin's provenance record to the trust policy
// of the day and to the cached evidence (REQ-dep-verify), reporting
// every mismatch and failing when any exists. Pairs with no cached
// artifacts are outside its scope, an uncached envelope the same way.
func Verify(ctx context.Context, s *Session, out io.Writer) error {
	// The modules' pins, then the rulesets' (REQ-dep-ruleset-
	// declarations), each list in raw-byte order of pair, a ruleset
	// pin's line saying so.
	type listed struct {
		lockfile.ModulePin
		mark string
	}
	var pins []listed
	for _, l := range []lockfile.Pins{s.Lock.ModulePins(), s.Lock.RulesetPins()} {
		sorted := l.All()
		slices.SortFunc(sorted, func(a, b lockfile.ModulePin) int {
			if c := bytes.Compare([]byte(a.Path), []byte(b.Path)); c != 0 {
				return c
			}
			return bytes.Compare([]byte(a.Version), []byte(b.Version))
		})
		mark := ""
		if l.Name() == "ruleset" {
			mark = " (ruleset pin)"
		}
		for _, p := range sorted {
			pins = append(pins, listed{p, mark})
		}
	}
	var mismatches []string
	verified := 0
	for _, pin := range pins {
		v, err := version.Parse(pin.Version)
		if err != nil {
			return fmt.Errorf("pin %s@%s%s: %w", pin.Path, pin.Version, pin.mark, err)
		}
		b, ok, err := s.Client.Cache.Get(pin.Path, v, fetch.KindZip, pin.Digest)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		digest, _, err := archive.DigestZip(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s@%s%s: cached archive unreadable: %v", pin.Path, pin.Version, pin.mark, err))
			continue
		}
		if digest != pin.Digest {
			mismatches = append(mismatches, fmt.Sprintf("%s@%s%s: digest %s, pin records %s", pin.Path, pin.Version, pin.mark, digest, pin.Digest))
			continue
		}
		if pin.Modfile != "" {
			mb, has, err := archive.ZipModuleFile(bytes.NewReader(b), int64(len(b)))
			if err != nil || !has {
				mismatches = append(mismatches, fmt.Sprintf("%s@%s%s: pinned module file missing from cached archive", pin.Path, pin.Version, pin.mark))
				continue
			}
			if got := fetch.ModfileHash(mb); got != pin.Modfile {
				mismatches = append(mismatches, fmt.Sprintf("%s@%s%s: modfile %s, pin records %s", pin.Path, pin.Version, pin.mark, got, pin.Modfile))
				continue
			}
		}
		if err := s.Client.VerifyCachedEvidence(ctx, pin.Path, v, pin.ModulePin, b); err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s@%s%s: provenance: %v", pin.Path, pin.Version, pin.mark, err))
			continue
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

// reresolve re-resolves one pinned pair named <path>@<version>: its
// pins — in every list that pins it, the modules' and the rulesets',
// one content under one digest (REQ-lock-ruleset-entry) — dropped
// together and the pair run through the first-use pipeline for each,
// fetched from the sources, its provenance judged under the trust
// policy of the day, pinned anew, each list's transition reported
// (REQ-dep-update). A pair no list pins is refused: there is nothing
// to re-resolve. A replaced path is refused as the module-path form
// refuses it: a replaced pair is never pinned, its replacement's is.
// The read goes where every pair's does — a module's through the
// driver, a ruleset's through the client's ruleset pipeline — the
// replaced path refused first, so the pair read is the pair named.
func reresolve(ctx context.Context, s *Session, out io.Writer, target string) error {
	path, ver, ok := strings.Cut(target, "@")
	if !ok || path == "" || ver == "" {
		return fmt.Errorf("dep update: %s is not a <path>@<version> pair", target)
	}
	v, err := version.Parse(ver)
	if err != nil {
		return fmt.Errorf("dep update: %s: %w", target, err)
	}
	if s.Root.Replaced(path) {
		return fmt.Errorf("dep update: %s is replaced by %s in the workspace file; a replaced pair is never pinned, its replacement's pair is", target, s.Root.Source(path, version.Version{}))
	}
	modPin, inModules := s.Lock.ModulePins().Module(path, v.String())
	rsPin, inRulesets := s.Lock.RulesetPins().Module(path, v.String())
	if !inModules && !inRulesets {
		return fmt.Errorf("dep update: no pin records %s; a pair is re-resolved, never pinned afresh here", target)
	}
	same := func(p lockfile.ModulePin) bool { return p.Path == path && p.Version == v.String() }
	s.Lock.Modules = slices.DeleteFunc(s.Lock.Modules, same)
	s.Lock.Rulesets = slices.DeleteFunc(s.Lock.Rulesets, same)
	report := func(before lockfile.ModulePin, list lockfile.Pins, mark string) error {
		after, ok := list.Module(path, v.String())
		if !ok {
			return fmt.Errorf("dep update: %s: the re-resolution recorded no %s pin", target, list.Name())
		}
		fmt.Fprintf(out, "%s%s: digest %s -> %s, provenance %s -> %s\n", target, mark, before.Digest, after.Digest, provenanceSpelling(before.Provenance), provenanceSpelling(after.Provenance))
		return nil
	}
	if inModules {
		if _, err := s.Driver.Download(ctx, mvs.Requirement{Path: path, Version: v}); err != nil {
			return fmt.Errorf("dep update: %s: %w", target, err)
		}
		if err := report(modPin, s.Lock.ModulePins(), ""); err != nil {
			return err
		}
	}
	if inRulesets {
		if err := s.Client.RulesetDownload(ctx, path, v); err != nil {
			return fmt.Errorf("dep update: %s: %w", target, err)
		}
		if err := report(rsPin, s.Lock.RulesetPins(), " (ruleset pin)"); err != nil {
			return err
		}
	}
	return nil
}
