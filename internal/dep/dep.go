// Package dep implements the dep verbs (dep-verbs.md): the user-facing
// operations over the resolution driver, the fetch-verify pipeline, and
// the pin store. Each verb is a function over a loaded Session so the
// command layer stays assembly-only: environment reading and seam
// construction happen once, in the caller, and every observable
// behavior lives here under test.
package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/proto/modfiles"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/mvs"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/resolve"
	"github.com/greatliontech/pb/internal/source/fetch"
)

// Config carries the assembled seams a Session is loaded over. WS is
// the writable working tree the workspace lives in; Dir the working
// directory within it; Client everything the pipeline needs except the
// pin store and trust policy, which Load reads from the resolution
// root itself.
type Config struct {
	WS     billy.Filesystem
	Dir    string
	Client *fetch.Client
}

// Session is one loaded resolution root: the workspace, its pin store
// and policy wired into the client, and the driver over both.
type Session struct {
	WS     billy.Filesystem
	Root   *workspace.Root
	Lock   *lockfile.File
	Client *fetch.Client
	Driver *resolve.Driver

	lockOrig []byte // the lockfile bytes as loaded; "" when absent
}

// Load locates the resolution root governing cfg.Dir (workspace.LoadFor,
// membership enforced), reads its lockfile and trust policy, and wires
// them into the client (dep-verbs.md, the dep verb term).
func Load(cfg Config) (*Session, error) {
	fsys := iofs.New(cfg.WS)
	root, err := workspace.LoadFor(fsys, cfg.Dir)
	if err != nil {
		return nil, err
	}
	s := &Session{WS: cfg.WS, Root: root, Client: cfg.Client}

	lockPath := path.Join(root.Dir, workspace.LockFileName)
	if b, err := fs.ReadFile(fsys, lockPath); err == nil {
		lf, err := lockfile.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lockPath, err)
		}
		s.Lock, s.lockOrig = lf, b
	} else if errors.Is(err, fs.ErrNotExist) {
		s.Lock = &lockfile.File{}
	} else {
		return nil, err
	}

	// The policy is the root's alone: reset before the conditional read
	// so a reused client never carries the previous root's trust
	// configuration into this session. An absent policy file is the
	// empty policy — the one place that default is folded, so no
	// consumer handles a nil policy.
	s.Client.Policy = &trust.Policy{}
	trustPath := path.Join(root.Dir, trust.FileName)
	if b, err := fs.ReadFile(fsys, trustPath); err == nil {
		p, err := trust.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", trustPath, err)
		}
		s.Client.Policy = p
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	s.Client.Lock = s.Lock
	s.Driver = &resolve.Driver{Root: root, Client: s.Client}
	return s, nil
}

// GenFile is the root's generation configuration, parsed
// (generation.md); a workspace has one at its root or none.
func (s *Session) GenFile() (*genfile.File, error) {
	data, err := util.ReadFile(s.WS, path.Join(s.Root.Dir, genfile.FileName))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", genfile.FileName, err)
	}
	return genfile.Parse(data)
}

// Modules is the build's modules: the build list resolved and every
// module's files loaded, an external through the client — verified
// and pinned — and the pins saved before anything follows, so a
// first-use resolution is the record whatever a verb does next
// (REQ-lock-first-use).
func (s *Session) Modules(ctx context.Context) ([]mvs.Requirement, []modfiles.Module, error) {
	list, _, err := s.Driver.BuildList(ctx)
	if err := savePins(s, err); err != nil {
		return nil, nil, err
	}
	mods, err := modfiles.Load(ctx, iofs.New(s.WS), s.Root, list, func(ctx context.Context, modPath string, v version.Version) ([]byte, error) {
		src, srcV := s.Root.Source(modPath, v)
		return s.Client.Zip(ctx, src, srcV)
	})
	if err := savePins(s, err); err != nil {
		return nil, nil, err
	}
	return list, mods, nil
}

// savePins saves the lockfile after a step that may have pinned,
// whatever the step's outcome: the step's error, joined with the
// save's where both fail; the save's alone; or nothing.
func savePins(s *Session, stepErr error) error {
	if saveErr := s.SaveLock(); saveErr != nil {
		if stepErr != nil {
			return fmt.Errorf("%w (and the lockfile could not be saved: %v)", stepErr, saveErr)
		}
		return saveErr
	}
	return stepErr
}

// SaveLock writes the lockfile canonically at the resolution root when
// its recorded facts changed (REQ-lock-canonical-emission): unchanged
// pins rewrite nothing.
func (s *Session) SaveLock() error {
	// A lockfile appears when a resolving verb first records a pin
	// (REQ-dep-init's second half): an empty pin store never creates
	// one.
	if s.lockOrig == nil && len(s.Lock.Modules) == 0 && len(s.Lock.Plugins) == 0 {
		return nil
	}
	b, err := lockfile.Encode(s.Lock)
	if err != nil {
		return err
	}
	if string(b) == string(s.lockOrig) {
		return nil
	}
	if err := writeFile(s.WS, path.Join(s.Root.Dir, workspace.LockFileName), b); err != nil {
		return err
	}
	s.lockOrig = b
	return nil
}

// writeFile writes bytes atomically through the shared discipline.
func writeFile(ws billy.Filesystem, name string, data []byte) error {
	return atomicfile.Write(ws, name, ".pb-", 0o644, data)
}

// Init writes a canonical module file declaring modulePath in dir
// (REQ-dep-init), failing when one already exists there or the path is
// invalid. It writes nothing else.
func Init(ws billy.Filesystem, dir, modulePath string) error {
	if err := module.ValidatePath(modulePath); err != nil {
		return err
	}
	target := path.Join(dir, module.ModuleFileName)
	if _, err := ws.Stat(target); err == nil {
		return fmt.Errorf("dep init: %s already exists", target)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b, err := modfile.Encode(&modfile.File{Module: modulePath})
	if err != nil {
		return err
	}
	return writeFile(ws, target, b)
}

// Download fetches, verifies, and pins every non-local build-list
// module's artifacts into the module cache and records the pins
// (REQ-dep-download).
func Download(ctx context.Context, s *Session, out io.Writer) error {
	list, crossings, err := s.Driver.BuildList(ctx)
	if err != nil {
		return err
	}
	warnCrossings(out, crossings)
	for _, r := range list {
		// A replaced path is never fetched: its replacement is what the
		// build reads, so that pair is the one downloaded, and the line
		// names both (REQ-work-replace).
		src, srcV := s.Root.Source(r.Path, r.Version)
		if err := s.Client.Download(ctx, src, srcV); err != nil {
			return err
		}
		if src != r.Path {
			fmt.Fprintf(out, "%s@%s => %s@%s\n", r.Path, r.Version, src, srcV)
			continue
		}
		fmt.Fprintf(out, "%s@%s\n", r.Path, r.Version)
	}
	return s.SaveLock()
}

// Graph prints the requirement graph, one edge per line
// (REQ-dep-graph).
func Graph(ctx context.Context, s *Session, out io.Writer) error {
	edges, err := s.Driver.Graph(ctx)
	if err != nil {
		return err
	}
	for _, e := range edges {
		fmt.Fprintf(out, "%s %s@%s\n", e.Requirer, e.Path, e.Version)
	}
	return s.SaveLock()
}

// Why prints, for each named path, a shortest requirement chain or
// that the module is not needed (REQ-dep-why). The graph is computed
// once and shared across targets.
func Why(ctx context.Context, s *Session, out io.Writer, targets ...string) error {
	edges, err := s.Driver.Graph(ctx)
	if err != nil {
		return err
	}
	for i, target := range targets {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "# %s\n", target)
		chain := resolve.WhyOver(edges, target)
		if chain == nil {
			fmt.Fprintf(out, "(module %s is not needed)\n", target)
			continue
		}
		for _, node := range chain {
			fmt.Fprintln(out, node)
		}
	}
	return s.SaveLock()
}

// warnCrossings reports accepted major crossings
// (REQ-resolve-major-crossing's warning half): the module, the
// selected version, and the lowest crossed requirement.
func warnCrossings(out io.Writer, crossings []mvs.Crossing) {
	for _, c := range crossings {
		if !c.RootAccepted {
			continue
		}
		fmt.Fprintf(out, "# warning: selecting %s %s crosses major above %s required by %s\n",
			c.Path, c.Selected, c.Crossed.Version, crossedName(c.Crossed.Requirer))
	}
}

func crossedName(r string) string {
	if r == "" {
		return "the root"
	}
	return r
}
