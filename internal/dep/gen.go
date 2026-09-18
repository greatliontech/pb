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
	"github.com/greatliontech/pb/internal/genfile"
	"github.com/greatliontech/pb/internal/genrequest"
	"github.com/greatliontech/pb/internal/modfiles"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/plugoci"
	"github.com/greatliontech/pb/internal/plugrun"
	"github.com/greatliontech/pb/internal/protocomp"
	"github.com/greatliontech/pb/internal/rootpath"
	"github.com/greatliontech/pb/internal/version"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/go-git/go-billy/v6/util"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// Acquirer materializes an oci-scheme plugin; plugoci.Acquirer is the
// production implementation, injected for the verb's own tests.
type Acquirer interface {
	Acquire(ctx context.Context, ref string) (*plugoci.Acquired, error)
}

// GenDeps are the seams the gen verb runs over.
type GenDeps struct {
	Acquirer Acquirer
	Runner   plugrun.Runner
}

// Gen is the generate verb (REQ-gen-verb): parse pb.gen.yaml, compile
// the build (REQ-gen-compile), acquire every entry's plugin — pins
// recorded on first use persist before anything executes — and then,
// entry by entry, build the request (REQ-gen-request), execute the
// plugin under the tier floor (REQ-plugin-min-tier,
// REQ-plugin-reported-tier), and write the response's files under the
// entry's out directory (REQ-gen-out-containment). Entry schemes are
// gated by the trust policy's execution posture.
func Gen(ctx context.Context, s *Session, deps GenDeps, out io.Writer) error {
	data, err := util.ReadFile(s.WS, path.Join(s.Root.Dir, genfile.FileName))
	if err != nil {
		return fmt.Errorf("generate: reading %s: %w", genfile.FileName, err)
	}
	gf, err := genfile.Parse(data)
	if err != nil {
		return err
	}
	exec := &s.Client.Policy.Execution
	for _, p := range gf.Plugins {
		if !exec.SchemeAllowed(p.Scheme) {
			return fmt.Errorf("generate: the trust policy does not permit %s-scheme plugins (plugin %s)", p.Scheme, p.Ref)
		}
		if p.Scheme != plugexec.SchemeOCI {
			return fmt.Errorf("generate: local plugins are not yet supported (plugin %s)", p.Ref)
		}
	}

	list, _, err := s.Driver.BuildList(ctx)
	if err != nil {
		return err
	}
	mods, err := modfiles.Load(ctx, iofs.New(s.WS), s.Root, list, func(ctx context.Context, modPath string, v version.Version) ([]byte, error) {
		return s.Client.Zip(ctx, modPath, v)
	})
	if err != nil {
		return err
	}
	compiled, err := protocomp.Compile(ctx, mods)
	if err != nil {
		return err
	}

	// Every plugin is acquired — verified and pinned — before any
	// runs, and the pins persist whatever follows: a first-use
	// resolution is the record even when a later entry fails
	// (REQ-plugin-digest-pin, REQ-lock-first-use).
	acquired := make([]*plugoci.Acquired, len(gf.Plugins))
	var acqErr error
	for i, entry := range gf.Plugins {
		acquired[i], acqErr = deps.Acquirer.Acquire(ctx, entry.Ref)
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
	for i, entry := range gf.Plugins {
		req, err := genrequest.Build(compiled.Files, gf.Overrides, entry.Opt)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		reqBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		res, err := deps.Runner.Run(ctx, plugrun.Spec{
			Rootfs:  acquired[i].Rootfs,
			Process: acquired[i].Process,
			Stdin:   reqBytes,
			Limits:  limits,
		})
		if err != nil {
			return fmt.Errorf("generate: plugin %s: %w", entry.Ref, err)
		}
		// The runner's report is the record the tier floor and the
		// bounds clause are judged on; a runner that reports neither
		// has broken its contract.
		if !plugexec.ValidTier(res.Tier) || res.Bounds == "" {
			return fmt.Errorf("generate: plugin %s: the runner reported no sandbox tier or bounds mechanism (tier %q, bounds %q)", entry.Ref, res.Tier, res.Bounds)
		}
		if plugexec.TierBelow(res.Tier, minTier) {
			return fmt.Errorf("generate: plugin %s ran at tier %s, below the required %s; lower the floor explicitly in the trust policy's execution block to accept this", entry.Ref, res.Tier, minTier)
		}
		resp, err := plugrun.Respond(res)
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
