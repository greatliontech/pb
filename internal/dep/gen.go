package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/plugin/genrequest"
	"github.com/greatliontech/pb/internal/plugin/runner"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/rootpath"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// Acquirer yields an entry's plugin from the entry's value, an oci
// reference; oci.Acquirer is the production implementation, injected
// for the verb's own tests.
type Acquirer interface {
	Acquire(ctx context.Context, value string) (*plugin.Acquired, error)
}

// LocalAcquirer yields a local entry's plugin from its command and
// arguments; local.Acquirer is the production implementation.
type LocalAcquirer interface {
	Acquire(ctx context.Context, value string, args []string) (*plugin.Acquired, error)
}

// ImageAcquirer is the oci scheme's acquirer, which also honors an
// override: an entry's reference materialized from a source the
// invocation names in its place (REQ-plugin-override). What it yields
// is an image, so its results carry the image's world always, which
// the verb hands to the runner whole.
type ImageAcquirer interface {
	Acquirer
	AcquireOverride(ctx context.Context, ref, source string) (*plugin.Acquired, error)
}

// LocalDeps are the local scheme's seams, present together or not at
// all: the acquirer, and the native runner a host binary runs on —
// whichever runner the oci entries selected.
type LocalDeps struct {
	Acquirer LocalAcquirer
	Runner   runner.Runner
}

// GenDeps are the seams the gen verb runs over: the oci acquirer and
// the selected runner for oci entries, and the local scheme's pair
// where the host has a native runner — without one, no local plugin
// runs.
type GenDeps struct {
	Acquirer ImageAcquirer
	Runner   runner.Runner
	Local    *LocalDeps
	// Overrides are the invocation's plugin overrides, declared oci
	// reference to source (REQ-plugin-override): a path to an OCI
	// layout or archive, or "docker://IMAGE" for a daemon-local image.
	Overrides map[string]string
	// Diagnostics receives what the invocation reports on standard
	// error: each overridden entry; nil discards.
	Diagnostics io.Writer
}

// OverrideDaemonPrefix spells a daemon-local image as an override
// source (plugin-execution.md, REQ-plugin-override).
const OverrideDaemonPrefix = "docker://"

// Gen is the generate verb (REQ-gen-verb): parse pb.gen.yaml, compile
// the build (REQ-gen-compile), acquire every entry's plugin — pins
// recorded on first use persist before anything executes — and then,
// entry by entry, build the request (REQ-gen-request), execute the
// plugin under the tier floor (REQ-plugin-min-tier,
// REQ-plugin-reported-tier), and write the response's files under the
// entry's out directory (REQ-gen-out-containment). Entry schemes are
// gated by the trust policy's execution posture.
func Gen(ctx context.Context, s *Session, deps GenDeps, out io.Writer) error {
	gf, err := s.GenFile()
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	exec := &s.Client.Policy.Execution
	for _, p := range gf.Plugins {
		if !exec.SchemeAllowed(p.Scheme) {
			return fmt.Errorf("generate: the trust policy does not permit %s-scheme plugins (plugin %s)", p.Scheme, p.Command())
		}
		if p.Scheme == plugin.SchemeLocal && deps.Local == nil {
			return fmt.Errorf("generate: local plugins run on the native runner, and none is wired here (pb has one on Linux only) (plugin %s)", p.Command())
		}
	}
	_, daemonRunner := deps.Runner.(runner.DaemonImages)
	// Overrides are judged before anything is acquired: the policy
	// admits them or not, and every key names a declared oci entry
	// (REQ-plugin-override).
	if len(deps.Overrides) > 0 && !exec.OverridesAllowed() {
		return errors.New("generate: the trust policy forbids plugin overrides")
	}
	for key, source := range deps.Overrides {
		found := false
		for _, p := range gf.Plugins {
			found = found || (p.Scheme == plugin.SchemeOCI && p.Ref == key)
		}
		if !found {
			return fmt.Errorf("generate: override %s names no oci plugin entry", key)
		}
		if !daemonRunner && strings.HasPrefix(source, OverrideDaemonPrefix) {
			return fmt.Errorf("generate: override %s names a daemon-local image, which only the docker runner runs (select it with --%s docker)", key, runner.FlagRunner)
		}
	}
	diag := deps.Diagnostics
	if diag == nil {
		diag = io.Discard
	}

	_, mods, err := s.Modules(ctx)
	if err != nil {
		return err
	}
	compiled, err := compile.Compile(ctx, mods)
	if err != nil {
		return err
	}

	// Every plugin is acquired — verified and pinned — before any
	// runs, and the pins persist whatever follows: a first-use
	// resolution is the record even when a later entry fails
	// (REQ-plugin-digest-pin, REQ-lock-first-use).
	plugins := make([]*plugin.Acquired, len(gf.Plugins))
	var acqErr error
	for i, entry := range gf.Plugins {
		switch entry.Scheme {
		case plugin.SchemeLocal:
			plugins[i], acqErr = deps.Local.Acquirer.Acquire(ctx, entry.Ref, entry.Args)
		default:
			source, overridden := deps.Overrides[entry.Ref]
			switch {
			case overridden && strings.HasPrefix(source, OverrideDaemonPrefix):
				// A daemon-local image: the daemon's already, run by
				// the docker runner as it is; the process is the
				// image's own configuration, which the daemon applies.
				local := &plugin.DaemonLocal{Reference: strings.TrimPrefix(source, OverrideDaemonPrefix)}
				plugins[i] = &plugin.Acquired{Image: local}
				fmt.Fprintf(diag, "overriding %s with the daemon-local image %s\n", entry.Ref, local.Reference)
			case overridden:
				plugins[i], acqErr = deps.Acquirer.AcquireOverride(ctx, entry.Ref, source)
				if acqErr == nil {
					fmt.Fprintf(diag, "overriding %s with %s\n", entry.Ref, source)
				}
			default:
				plugins[i], acqErr = deps.Acquirer.Acquire(ctx, entry.Ref)
				if acqErr == nil {
					if pulled, ok := plugins[i].Image.(*plugin.Pulled); ok && !daemonRunner {
						// The acquisition yielded a digest for the
						// daemon to pull; the CLI refuses the byte path
						// for this runner first, so this is the seam's
						// own guard.
						acqErr = fmt.Errorf("the acquisition yields %s for a daemon to pull, and the selected runner runs no daemon images", pulled.Reference())
					}
				}
			}
		}
		if acqErr != nil {
			acqErr = fmt.Errorf("generate: plugin %s: %w", entry.Command(), acqErr)
			break
		}
	}
	if err := s.SaveLock(); err != nil {
		if acqErr != nil {
			return fmt.Errorf("%w (and the lockfile could not be saved: %v)", acqErr, err)
		}
		return err
	}
	if acqErr != nil {
		return acqErr
	}

	// Every request is built before anything is emptied or run: a
	// dead pattern or an override naming nothing refuses while the
	// output directories still hold what they held (REQ-gen-clean).
	requests := make([][]byte, len(gf.Plugins))
	for i, entry := range gf.Plugins {
		req, err := genrequest.Build(compiled.Topological(), compiled.Files, gf.Overrides, entry, modfiles.WellKnown)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Command(), err)
		}
		marshal := proto.MarshalOptions{Deterministic: true}
		if requests[i], err = marshal.Marshal(req); err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Command(), err)
		}
	}
	if err := s.cleanOutputs(gf, mods); err != nil {
		return err
	}
	limits := exec.EffectiveLimits()
	minTier := exec.EffectiveMinTier()
	// The one way past a tier refusal (REQ-plugin-min-tier).
	const lowerFloorHint = "lower the floor explicitly in the trust policy's execution block to accept a weaker tier"
	for i, entry := range gf.Plugins {
		reqBytes := requests[i]
		// A local entry runs on the native runner under no floor: the
		// policy admitted the scheme as a downgrade of every guarantee
		// a sandbox row gives an image, so whatever row the host puts
		// around the binary is reported and none is required
		// (plugin-execution.md, "Local binaries").
		run, floor := deps.Runner, minTier
		if entry.Scheme == plugin.SchemeLocal {
			run, floor = deps.Local.Runner, plugin.TierNone
		}
		// The world the acquisition yielded is the world the run is
		// in, whole: the runner reads of it what its substrate needs.
		spec := runner.Spec{Scheme: entry.Scheme, Image: plugins[i].Image, Process: plugins[i].Process, Stdin: reqBytes, Limits: limits, MinTier: floor}
		res, err := run.Run(ctx, spec)
		if errors.Is(err, runner.ErrTierUnreachable) {
			return fmt.Errorf("generate: plugin %s: %w; %s", entry.Command(), err, lowerFloorHint)
		}
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Command(), err)
		}
		// The runner's report is the record the tier floor and the
		// bounds clause are judged on; a runner that reports neither
		// has broken its contract.
		if !plugin.ValidTier(res.Tier) || res.Bounds == "" {
			return fmt.Errorf("generate: plugin %s: the runner reported no sandbox tier or bounds mechanism (tier %q, bounds %q)", entry.Command(), res.Tier, res.Bounds)
		}
		if plugin.TierBelow(res.Tier, floor) {
			return fmt.Errorf("generate: plugin %s ran at tier %s, below the required %s; %s", entry.Command(), res.Tier, floor, lowerFloorHint)
		}
		resp, err := runner.Respond(res)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Command(), err)
		}
		n, err := s.writeGenerated(entry, resp)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Command(), err)
		}
		fmt.Fprintf(out, "%s: %d file(s) into %s (tier %s, bounds %s)\n", entry.Command(), n, entry.Out, res.Tier, res.Bounds)
	}
	return nil
}

// cleanOutputs empties every output directory the entries clean
// names (REQ-gen-clean), in entry order, every entry under it removed and
// the directory kept — a directory named twice is emptied twice,
// which nothing observes, no plugin having run; refused before
// anything is removed, naming the entry, where a directory is the
// resolution root, is or holds the directory of a module read from
// the working tree, holds any of its files, holds a module file at
// any depth, the build's or not, is reached through a symlink, is
// no directory, or is or holds the output directory of an entry
// clean does not name — what generation never wrote is not its to
// remove, and a directory asked to be emptied and kept at once is a
// contradiction; one holding a cleaned directory is kept itself,
// the cleaned directory emptied under it.
func (s *Session) cleanOutputs(gf *genfile.File, mods []modfiles.Module) error {
	// The entries clean names: every one under the file's clean, else
	// those with their own (REQ-gen-clean).
	var cleaning []genfile.Plugin
	for _, entry := range gf.Plugins {
		if gf.Clean || entry.Clean {
			cleaning = append(cleaning, entry)
		}
	}
	for _, entry := range cleaning {
		if entry.Out == "." {
			return fmt.Errorf("generate: plugin %s: clean refuses to empty the resolution root (out %q)", entry.Command(), entry.Out)
		}
		for _, kept := range gf.Plugins {
			if gf.Clean || kept.Clean {
				continue
			}
			switch {
			case kept.Out == entry.Out:
				return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, which is the output directory of the plugin %s, which clean does not name", entry.Command(), entry.Out, kept.Command())
			case rootpath.Contains(entry.Out, kept.Out):
				return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, which holds the output directory %s of the plugin %s, which clean does not name", entry.Command(), entry.Out, kept.Out, kept.Command())
			}
		}
		for _, m := range mods {
			if m.Dir == "" {
				continue
			}
			if m.Dir == entry.Out || rootpath.Contains(entry.Out, m.Dir) {
				return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, which holds the module %s at %s", entry.Command(), entry.Out, m.Path, m.Dir)
			}
			if f := moduleFileUnder(m, entry.Out); f != "" {
				return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, which holds %s of the module %s", entry.Command(), entry.Out, f, m.Path)
			}
		}
		// Every rule above judges the directory by its name, and the
		// removal touches what the name reaches: a link anywhere on
		// the way would part the two.
		link, err := s.symlinkedComponent(s.Root.Dir, entry.Out, nil)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: clean: %w", entry.Command(), err)
		}
		if link != "" {
			return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, reached through the symlink %s", entry.Command(), entry.Out, link)
		}
		if fi, err := s.WS.Stat(path.Join(s.Root.Dir, entry.Out)); err == nil && !fi.IsDir() {
			return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, which is not a directory", entry.Command(), entry.Out)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("generate: plugin %s: clean: %w", entry.Command(), err)
		}
		found, err := s.moduleFileWithin(path.Join(s.Root.Dir, entry.Out))
		if err != nil {
			return fmt.Errorf("generate: plugin %s: clean: %w", entry.Command(), err)
		}
		if found != "" {
			return fmt.Errorf("generate: plugin %s: clean refuses to empty %s, which holds the module file %s", entry.Command(), entry.Out, found)
		}
	}
	for _, entry := range cleaning {
		dir := path.Join(s.Root.Dir, entry.Out)
		entries, err := s.WS.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("generate: plugin %s: clean: %w", entry.Command(), err)
		}
		for _, e := range entries {
			if err := util.RemoveAll(s.WS, path.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("generate: plugin %s: clean: %w", entry.Command(), err)
			}
		}
	}
	return nil
}

// moduleFileUnder is a file of the working-tree module lying under
// dir — a protobuf source or a rule file, at its root-relative path —
// or "" where none does; the smallest such path, so the answer is
// deterministic.
func moduleFileUnder(m modfiles.Module, dir string) string {
	found := ""
	for _, set := range []map[string][]byte{m.Files, m.Rules} {
		for rel := range set {
			p := path.Join(m.Dir, rel)
			if rootpath.Contains(dir, p) && (found == "" || p < found) {
				found = p
			}
		}
	}
	return found
}

// moduleFileWithin walks dir for a module file at any depth and
// returns the first found in walk order, "" where none is; a dir
// that does not exist holds none.
func (s *Session) moduleFileWithin(dir string) (string, error) {
	var found string
	errFound := errors.New("found")
	err := util.Walk(s.WS, dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && path.Base(p) == module.ModuleFileName {
			found = p
			return errFound
		}
		return nil
	})
	switch {
	case errors.Is(err, errFound):
		return found, nil
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist):
		return "", nil
	}
	return "", err
}

// writeGenerated lands a response's files under the entry's out
// directory (REQ-gen-out-containment): every name must be a clean
// relative forward-slash path naming a file strictly inside it, named
// once, reached through no symlinked directory, and insertion points
// are unsupported — pb never patches content it did not verify. The
// whole response is validated before anything is written; each write
// is per-file atomic (temp file + rename), but the response as a whole
// is not — a failure mid-response leaves the files already written,
// and rerunning gen regenerates them.
func (s *Session) writeGenerated(entry genfile.Plugin, resp *pluginpb.CodeGeneratorResponse) (int, error) {
	outDir := path.Join(s.Root.Dir, entry.Out)
	seen := make(map[string]bool, len(resp.GetFile()))
	checkedDirs := map[string]bool{}
	for _, f := range resp.GetFile() {
		name := f.GetName()
		if f.InsertionPoint != nil {
			return 0, fmt.Errorf("response file %q carries an insertion point, which pb does not support", name)
		}
		// The shared containment rule admits the root itself; a
		// response file must name a file, so "." is refused here.
		err := rootpath.Check(name, "the output directory")
		if err == nil && name == "." {
			err = errors.New("names the directory itself")
		}
		if err != nil {
			return 0, fmt.Errorf("response file %q is not a clean relative path inside the output directory: %w", name, err)
		}
		if seen[name] {
			return 0, fmt.Errorf("response names file %q twice", name)
		}
		seen[name] = true
		if err := s.refuseSymlinkedDirs(outDir, name, checkedDirs); err != nil {
			return 0, err
		}
	}
	for _, f := range resp.GetFile() {
		// billy filesystems create missing parent directories on file
		// creation, so the write needs no MkdirAll.
		target := path.Join(outDir, f.GetName())
		if err := atomicfile.Write(s.WS, target, ".pb-gen-", 0o644, []byte(f.GetContent())); err != nil {
			return 0, err
		}
	}
	return len(resp.GetFile()), nil
}

// refuseSymlinkedDirs refuses a response name whose directory
// components, below the output directory, pass through an existing
// symlink: containment is judged on the written name, and a symlinked
// directory would carry the write wherever it points. The output
// directory itself and everything above it are the author's own
// layout and are not judged; the leaf needs no check because the
// atomic rename replaces a symlink rather than following it.
func (s *Session) refuseSymlinkedDirs(outDir, name string, checked map[string]bool) error {
	link, err := s.symlinkedComponent(outDir, path.Dir(name), checked)
	if err != nil {
		return err
	}
	if link != "" {
		return fmt.Errorf("response file %q passes through %q, a symlink; generated files land only under the declared output directory", name, link)
	}
	return nil
}

// symlinkedComponent walks rel's components under base and returns
// the first that is a symbolic link, relative to base, or "" where
// none is; a component that does not exist is none. checked, where
// given, memoizes the directories found to be no link across calls.
func (s *Session) symlinkedComponent(base, rel string, checked map[string]bool) (string, error) {
	dir := base
	for _, seg := range strings.Split(rel, "/") {
		if seg == "." {
			continue
		}
		dir = path.Join(dir, seg)
		if checked[dir] {
			continue
		}
		fi, err := s.WS.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", err
		case fi.Mode()&fs.ModeSymlink != 0:
			return strings.TrimPrefix(dir, base+"/"), nil
		}
		if checked != nil {
			checked[dir] = true
		}
	}
	return "", nil
}
