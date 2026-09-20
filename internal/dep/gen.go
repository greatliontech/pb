package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/plugin/genrequest"
	"github.com/greatliontech/pb/internal/plugin/runner"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/rootpath"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// Acquirer yields an entry's plugin from the entry's value — an oci
// reference or a local binary's name — whichever scheme it acquires
// for; oci.Acquirer and local.Acquirer are the production
// implementations, injected for the verb's own tests.
type Acquirer interface {
	Acquire(ctx context.Context, value string) (*plugin.Acquired, error)
}

// ImageAcquirer is the oci scheme's acquirer, which also honors an
// override: an entry's reference materialized from a source the
// invocation names in its place (REQ-plugin-override). What it yields
// is an image, so its results carry the image's facts always; the
// verb reads them without a guard.
type ImageAcquirer interface {
	Acquirer
	AcquireOverride(ctx context.Context, ref, source string) (*plugin.Acquired, error)
}

// LocalDeps are the local scheme's seams, present together or not at
// all: the acquirer, and the native runner a host binary runs on —
// whichever runner the oci entries selected.
type LocalDeps struct {
	Acquirer Acquirer
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
			return fmt.Errorf("generate: the trust policy does not permit %s-scheme plugins (plugin %s)", p.Scheme, p.Ref)
		}
		if p.Scheme == plugin.SchemeLocal && deps.Local == nil {
			return fmt.Errorf("generate: local plugins run on the native runner, and none is wired here (pb has one on Linux only) (plugin %s)", p.Ref)
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
			plugins[i], acqErr = deps.Local.Acquirer.Acquire(ctx, entry.Ref)
		default:
			source, overridden := deps.Overrides[entry.Ref]
			switch {
			case overridden && strings.HasPrefix(source, OverrideDaemonPrefix):
				// A daemon-local image: the daemon's already, run by
				// the docker runner as it is; the process is the
				// image's own configuration, which the daemon applies.
				plugins[i] = &plugin.Acquired{Image: &plugin.Image{Reference: strings.TrimPrefix(source, OverrideDaemonPrefix)}}
				fmt.Fprintf(diag, "overriding %s with the daemon-local image %s\n", entry.Ref, plugins[i].Image.Reference)
			case overridden:
				plugins[i], acqErr = deps.Acquirer.AcquireOverride(ctx, entry.Ref, source)
				if acqErr == nil {
					fmt.Fprintf(diag, "overriding %s with %s\n", entry.Ref, source)
				}
			default:
				plugins[i], acqErr = deps.Acquirer.Acquire(ctx, entry.Ref)
				if acqErr == nil && plugins[i].Image.Pull && !daemonRunner {
					// The acquisition yielded a digest for the daemon
					// to pull; the CLI refuses the byte path for this
					// runner first, so this is the seam's own guard.
					acqErr = fmt.Errorf("the acquisition yields %s for a daemon to pull, and the selected runner runs no daemon images", plugins[i].Image.Reference)
				}
			}
		}
		if acqErr != nil {
			acqErr = fmt.Errorf("generate: plugin %s: %w", entry.Ref, acqErr)
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

	limits := exec.EffectiveLimits()
	minTier := exec.EffectiveMinTier()
	// The one way past a tier refusal (REQ-plugin-min-tier).
	const lowerFloorHint = "lower the floor explicitly in the trust policy's execution block to accept a weaker tier"
	for i, entry := range gf.Plugins {
		req, err := genrequest.Build(compiled.Files, gf.Overrides, entry.Opt)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		reqBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		// A local entry runs on the native runner under no floor: the
		// policy admitted the scheme as a downgrade of every guarantee
		// a sandbox row gives an image, so whatever row the host puts
		// around the binary is reported and none is required
		// (plugin-execution.md, "Local binaries").
		run, floor := deps.Runner, minTier
		if entry.Scheme == plugin.SchemeLocal {
			run, floor = deps.Local.Runner, plugin.TierNone
		}
		spec := runner.Spec{Scheme: entry.Scheme, Process: plugins[i].Process, Stdin: reqBytes, Limits: limits, MinTier: floor}
		if img := plugins[i].Image; img != nil {
			// The admitted entry's platform is the daemon's to be
			// told for a pulled image; an export already is that
			// child.
			spec.Rootfs, spec.Reference, spec.Pull = img.Rootfs, img.Reference, img.Pull
			if spec.Pull {
				spec.Entry = img.Entry
			}
		}
		res, err := run.Run(ctx, spec)
		if errors.Is(err, runner.ErrTierUnreachable) {
			return fmt.Errorf("generate: plugin %s: %w; %s", entry.Ref, err, lowerFloorHint)
		}
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		// The runner's report is the record the tier floor and the
		// bounds clause are judged on; a runner that reports neither
		// has broken its contract.
		if !plugin.ValidTier(res.Tier) || res.Bounds == "" {
			return fmt.Errorf("generate: plugin %s: the runner reported no sandbox tier or bounds mechanism (tier %q, bounds %q)", entry.Ref, res.Tier, res.Bounds)
		}
		if plugin.TierBelow(res.Tier, floor) {
			return fmt.Errorf("generate: plugin %s ran at tier %s, below the required %s; %s", entry.Ref, res.Tier, floor, lowerFloorHint)
		}
		resp, err := runner.Respond(res)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		n, err := s.writeGenerated(entry, resp)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		fmt.Fprintf(out, "%s: %d file(s) into %s (tier %s, bounds %s)\n", entry.Ref, n, entry.Out, res.Tier, res.Bounds)
	}
	return nil
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
		// creation (TempFile included), so the write needs no MkdirAll.
		target := path.Join(outDir, f.GetName())
		if err := atomicfile.Write(s.WS, target, ".pb-gen-", []byte(f.GetContent())); err != nil {
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
	dir := outDir
	for _, seg := range strings.Split(path.Dir(name), "/") {
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
			return err
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("response file %q passes through %q, a symlink; generated files land only under the declared output directory", name, strings.TrimPrefix(dir, outDir+"/"))
		}
		checked[dir] = true
	}
	return nil
}
