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
	"slices"
	"strings"

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
	// ReadOnly loads the session at the lockfile's pins alone: a pair
	// the lockfile does not pin is refused (fetch.UnpinnedError), never
	// fetched and pinned, and the lockfile is never written — the
	// language server's session (lsp.md REQ-lsp-session), the verbs'
	// being read-write (REQ-lock-first-use).
	ReadOnly bool
}

// Session is one loaded resolution root: the workspace, its pin store
// and policy wired into the client, and the driver over both.
type Session struct {
	Tree
	Lock   *lockfile.File
	Client *fetch.Client
	Driver *resolve.Driver

	lockOrig []byte        // the lockfile bytes as loaded; "" when absent
	readOnly bool          // the lockfile never written (Config.ReadOnly)
	policy   *trust.Policy // the root's trust policy, the client's while the session is wired
}

// Load locates the resolution root governing cfg.Dir (workspace.LoadFor,
// membership enforced), reads its lockfile and trust policy, and wires
// them into the client (dep-verbs.md, the dep verb term): LoadRoot,
// then LoadFrom.
func Load(cfg Config) (*Session, error) {
	root, err := LoadRoot(cfg)
	if err != nil {
		return nil, err
	}
	return LoadFrom(root, cfg)
}

// LoadRoot locates and loads the resolution root governing cfg.Dir,
// as workspace.md reads it, membership enforced: the tree's part of a
// session, which a reader of the tree alone — the language server's
// own-file judgement — takes without the rest.
func LoadRoot(cfg Config) (*workspace.Root, error) {
	return workspace.LoadFor(iofs.New(cfg.WS), cfg.Dir)
}

// LoadFrom is the session of a loaded root: its lockfile and trust
// policy read and wired into the client.
func LoadFrom(root *workspace.Root, cfg Config) (*Session, error) {
	fsys := iofs.New(cfg.WS)
	s := &Session{Tree: Tree{WS: cfg.WS, Root: root}, Client: cfg.Client, readOnly: cfg.ReadOnly}

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

	// The policy is the root's alone. An absent policy file is the
	// empty policy — the one place that default is folded, so no
	// consumer handles a nil policy.
	policy := &trust.Policy{}
	trustPath := path.Join(root.Dir, trust.FileName)
	if b, err := fs.ReadFile(fsys, trustPath); err == nil {
		p, err := trust.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", trustPath, err)
		}
		policy = p
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	// The client is wired to this session only once the whole load
	// succeeded: a reused client — the language server's, across
	// reloads — keeps serving the last session loaded where this one
	// fails, and never carries one root's policy, pins or mode into
	// the next.
	s.policy = policy
	s.Wire()
	return s, nil
}

// Wire wires the session's client to this session — its trust policy,
// its pin store, its mode — the one place the client is set from a
// session: Load's end, and a reader keeping an earlier session after a
// later load rewired the client and was then discarded (the language
// server's failed reload after a superseded one).
func (s *Session) Wire() {
	s.Client.Policy = s.policy
	s.Client.Lock = s.Lock
	s.Client.ReadOnly = s.readOnly
	s.Driver = &resolve.Driver{Root: s.Root, Client: s.Client}
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
	mods, err := modfiles.Load(ctx, iofs.New(s.WS), s.Root, list, s.Client.Zip)
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
	// A read-only session records no pin and rewrites nothing, not even
	// a lockfile whose spelling is not canonical (REQ-lsp-tree-untouched).
	if s.readOnly {
		return nil
	}
	// A lockfile appears when a resolving verb first records a pin
	// (REQ-dep-init's second half): an empty pin store never creates
	// one.
	if s.lockOrig == nil && len(s.Lock.Modules) == 0 && len(s.Lock.Rulesets) == 0 && len(s.Lock.Plugins) == 0 {
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
		// build reads, so that source is the one downloaded, and the
		// line names both (REQ-work-replace, REQ-work-replace-dir).
		src, err := s.Driver.Download(ctx, r)
		if err != nil {
			return err
		}
		if s.Root.Replaced(r.Path) {
			fmt.Fprintln(out, src.Label(r.Path, r.Version.String()))
			continue
		}
		fmt.Fprintf(out, "%s@%s\n", r.Path, r.Version)
	}
	// Then every fetched ruleset import, the lint file's and the rule
	// files', its artifacts the same, its line a module's, pinned as a
	// ruleset (REQ-dep-ruleset-declarations): each pair once, in the
	// order the imports are read.
	loaded, err := s.closure(ctx)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, e := range loaded.Edges {
		if e.Version == "" || done[e.To()] {
			continue
		}
		done[e.To()] = true
		parsed, err := version.Parse(e.Version)
		if err != nil {
			return err
		}
		src := s.Root.Source(e.Path, parsed)
		err = s.Client.RulesetDownload(ctx, src.Path, src.Version)
		if err := savePins(s, err); err != nil {
			return err
		}
		if s.Root.Replaced(e.Path) {
			fmt.Fprintln(out, src.Label(e.Path, e.Version))
			continue
		}
		fmt.Fprintln(out, e.To())
	}
	return s.SaveLock()
}

// importEdges is the requirement graph's edges the imports add
// (REQ-dep-ruleset-declarations): one per import, from the lint file
// or from the importing rule file's ruleset, a fetched import's to
// its pair as written, a working-tree import's to the bare path.
func (s *Session) importEdges(ctx context.Context) ([]mvs.Edge, error) {
	loaded, err := s.closure(ctx)
	if err != nil {
		return nil, err
	}
	var edges []mvs.Edge
	for _, e := range loaded.Edges {
		edge := mvs.Edge{Requirer: e.From, Path: e.Path}
		if e.Version != "" {
			parsed, err := version.Parse(e.Version)
			if err != nil {
				return nil, err
			}
			edge.Version = parsed
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

// edgeLine spells an edge as graph prints it: a working-tree import's
// target the bare path, the working copy having no version.
func edgeLine(e mvs.Edge) string {
	if e.Version == (version.Version{}) {
		return e.Requirer + " " + e.Path
	}
	return fmt.Sprintf("%s %s@%s", e.Requirer, e.Path, e.Version)
}

// Graph prints the requirement graph, one edge per line
// (REQ-dep-graph).
func Graph(ctx context.Context, s *Session, out io.Writer) error {
	edges, err := s.graph(ctx)
	if err != nil {
		return err
	}
	for _, e := range edges {
		fmt.Fprintln(out, edgeLine(e))
	}
	return s.SaveLock()
}

// graph is the requirement graph with the lint file's import edges,
// sorted lexically by line as graph prints them.
func (s *Session) graph(ctx context.Context) ([]mvs.Edge, error) {
	edges, err := s.Driver.Graph(ctx)
	if err != nil {
		return nil, err
	}
	imports, err := s.importEdges(ctx)
	if err != nil {
		return nil, err
	}
	edges = append(edges, imports...)
	slices.SortFunc(edges, func(a, b mvs.Edge) int { return strings.Compare(edgeLine(a), edgeLine(b)) })
	return edges, nil
}

// Why prints, for each named path, a shortest requirement chain or
// that the module is not needed (REQ-dep-why). The graph is computed
// once for every target. A chain ending at a replaced path carries the
// replacement as its last line, and a replacement's own path answers,
// after any chain of its own, through each path it stands for: a
// pinned replacement is needed by what it replaces.
func Why(ctx context.Context, s *Session, out io.Writer, targets ...string) error {
	edges, err := s.graph(ctx)
	if err != nil {
		return err
	}
	// A chain to a replaced path ends in the replacement step, spelled
	// as download's line spells the source.
	// A working-tree import's edge names no pair: it is answered as
	// one step from the lint file beside the search over pairs, the
	// shorter chain winning, the lexically least at equal length.
	pairs := slices.DeleteFunc(slices.Clone(edges), func(e mvs.Edge) bool { return e.Version == (version.Version{}) })
	printChain := func(target string) bool {
		chain := resolve.WhyOver(pairs, target)
		for _, e := range edges {
			if e.Requirer != lintRequirer || e.Path != target || e.Version != (version.Version{}) {
				continue
			}
			imported := []string{lintRequirer, target}
			if chain == nil || len(imported) < len(chain) || len(imported) == len(chain) && slices.Compare(imported, chain) < 0 {
				chain = imported
			}
		}
		if chain == nil {
			return false
		}
		for _, node := range chain {
			fmt.Fprintln(out, node)
		}
		if s.Root.Replaced(target) {
			// The chain's last node is the target, at its selected
			// version where the chain reached a pair.
			selected := strings.TrimPrefix(strings.TrimPrefix(chain[len(chain)-1], target), "@")
			fmt.Fprintln(out, s.Root.Source(target, version.Version{}).Label(target, selected))
		}
		return true
	}
	for i, target := range targets {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "# %s\n", target)
		needed := printChain(target)
		for _, replaced := range s.Root.ReplacedBy(target) {
			needed = printChain(replaced) || needed
		}
		if !needed {
			fmt.Fprintf(out, "(module %s is not needed)\n", target)
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
