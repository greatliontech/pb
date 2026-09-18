package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/greatliontech/pb/internal/modfile"
	"github.com/greatliontech/pb/internal/rapidtest"
	"github.com/greatliontech/stipulator/stipulate/structural"
	"pgregory.net/rapid"
)

func TestMain(m *testing.M) { rapidtest.Main(m) }

func TestParse(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		f, err := Parse([]byte("use:\n  - .\n  - services/api\n"))
		if err != nil {
			t.Fatalf("Parse = %v, want nil", err)
		}
		if len(f.Use) != 2 || f.Use[0] != "." || f.Use[1] != "services/api" {
			t.Fatalf("use = %v", f.Use)
		}
	})
	invalid := map[string]struct {
		data    string
		wantSub string
	}{
		"empty file":            {"", "missing use key"},
		"comment-only file":     {"# nothing\n", "missing use key"},
		"empty use list":        {"use: []\n", "missing use key"},
		"unknown key":           {"use: [.]\nextra: 1\n", `unknown key "extra"`},
		"use not a list":        {"use: yes\n", "use must be a list"},
		"entry not a string":    {"use:\n  - [a]\n", "use[0] must be a string"},
		"empty entry":           {"use:\n  - \"\"\n", "empty path"},
		"absolute entry":        {"use:\n  - /abs\n", "is absolute"},
		"escaping entry":        {"use:\n  - ../out\n", "escapes the workspace root"},
		"bare parent entry":     {"use:\n  - ..\n", "escapes the workspace root"},
		"cleaned escape":        {"use:\n  - a/../../out\n", "escapes the workspace root"},
		"duplicate entry":       {"use:\n  - a\n  - a\n", "duplicate directory"},
		"clean-equal duplicate": {"use:\n  - a\n  - a/.\n", "duplicate directory"},
		"yaml alias":            {"x: &a 1\nuse: [*a]\n", "forbidden YAML construct"},
	}
	for name, c := range invalid {
		t.Run("invalid: "+name, func(t *testing.T) {
			_, err := Parse([]byte(c.data))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("Parse = %v, want ErrInvalid containing %q", err, c.wantSub)
			}
		})
	}
}

func modYAML(path string, deps map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "module: %s\n", path)
	if len(deps) > 0 {
		b.WriteString("deps:\n")
		keys := make([]string, 0, len(deps))
		for k := range deps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, deps[k])
		}
	}
	return b.String()
}

func TestFind(t *testing.T) {
	fsys := fstest.MapFS{
		"ws/pb.work":              {Data: []byte("use: [app]\n")},
		"ws/app/pb.yaml":          {Data: []byte(modYAML("example.test/app", nil))},
		"ws/app/proto/x.proto":    {Data: []byte("syntax")},
		"lone/mod/pb.yaml":        {Data: []byte(modYAML("example.test/lone", nil))},
		"lone/mod/deep/dir/f":     {Data: []byte("x")},
		"bare/nothing/here.proto": {Data: []byte("syntax")},
	}
	t.Run("workspace above a module wins", func(t *testing.T) {
		dir, isWS, err := Find(fsys, "ws/app/proto")
		if err != nil || !isWS || dir != "ws" {
			t.Fatalf("Find = (%q, %v, %v), want (ws, true, nil)", dir, isWS, err)
		}
	})
	t.Run("module only is the single-module default", func(t *testing.T) {
		dir, isWS, err := Find(fsys, "lone/mod/deep/dir")
		if err != nil || isWS || dir != "lone/mod" {
			t.Fatalf("Find = (%q, %v, %v), want (lone/mod, false, nil)", dir, isWS, err)
		}
	})
	t.Run("neither is ErrNoRoot", func(t *testing.T) {
		if _, _, err := Find(fsys, "bare/nothing"); !errors.Is(err, ErrNoRoot) {
			t.Fatalf("Find = %v, want ErrNoRoot", err)
		}
	})
	t.Run("the nearest module file wins among nested modules", func(t *testing.T) {
		nested := fstest.MapFS{
			"mod/pb.yaml":            {Data: []byte(modYAML("example.test/outer", nil))},
			"mod/inner/pb.yaml":      {Data: []byte(modYAML("example.test/inner", nil))},
			"mod/inner/deep/x.proto": {Data: []byte("syntax")},
		}
		dir, isWS, err := Find(nested, "mod/inner/deep")
		if err != nil || isWS || dir != "mod/inner" {
			t.Fatalf("Find = (%q, %v, %v), want the nearest module root mod/inner", dir, isWS, err)
		}
	})
	t.Run("a failing filesystem surfaces its error", func(t *testing.T) {
		if _, _, err := Find(errFS{fsys, errors.New("disk gone")}, "ws/app/proto"); err == nil ||
			!strings.Contains(err.Error(), "disk gone") {
			t.Fatalf("Find = %v, want the filesystem's own error", err)
		}
	})
	t.Run("a filesystem failing only on module files surfaces its error", func(t *testing.T) {
		sel := selectFS{fsys, "pb.yaml", errors.New("modfile io")}
		if _, _, err := Find(sel, "lone/mod/deep/dir"); err == nil ||
			!strings.Contains(err.Error(), "modfile io") {
			t.Fatalf("Find = %v, want the module-file stat error", err)
		}
	})
	t.Run("a filesystem failing only on workspace files surfaces its error", func(t *testing.T) {
		// The module-file probe succeeds here, so a swallowed
		// workspace-file failure could not hide behind it.
		sel := selectFS{fsys, "pb.work", errors.New("work io")}
		if _, _, err := Find(sel, "lone/mod/deep/dir"); err == nil ||
			!strings.Contains(err.Error(), "work io") {
			t.Fatalf("Find = %v, want the workspace-file stat error", err)
		}
	})
}

// errFS fails every open with err; selectFS fails only paths with the
// given suffix. Both make the not-exist-tolerant stat paths' error
// arms reachable.
type errFS struct {
	fs.FS
	err error
}

func (e errFS) Open(string) (fs.File, error) { return nil, e.err }

type selectFS struct {
	fs.FS
	suffix string
	err    error
}

func (s selectFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, s.suffix) {
		return nil, s.err
	}
	return s.FS.Open(name)
}

func TestLoad(t *testing.T) {
	t.Run("happy: workspace with two modules", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":     {Data: []byte("use:\n  - app\n  - lib\n")},
			"ws/app/pb.yaml": {Data: []byte(modYAML("example.test/app", map[string]string{"example.test/lib": "v1.0.0", "example.test/ext": "v1.2.0"}))},
			"ws/lib/pb.yaml": {Data: []byte(modYAML("example.test/lib", map[string]string{"example.test/ext": "v1.5.0"}))},
			"ws/pb.lock":     {Data: []byte("version: 1\nmodules: []\n")},
		}
		r, err := Load(fsys, "ws")
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if len(r.Modules) != 2 || r.File == nil {
			t.Fatalf("root = %+v", r)
		}
		if dir, ok := r.IsLocal("example.test/lib"); !ok || dir != "lib" {
			t.Fatalf("IsLocal(lib) = (%q, %v)", dir, ok)
		}
		if _, ok := r.IsLocal("example.test/ext"); ok {
			t.Fatal("IsLocal(ext) = true, want false")
		}
		union, err := r.Requirements()
		if err != nil {
			t.Fatalf("Requirements = %v", err)
		}
		// Both declared edges on ext survive: reachable-graph MVS
		// traverses superseded pairs' requirements, so the union carries
		// every edge, never a per-path maximum.
		if len(union) != 2 ||
			union[0].Path != "example.test/ext" || union[0].Version.String() != "v1.2.0" ||
			union[1].Path != "example.test/ext" || union[1].Version.String() != "v1.5.0" {
			t.Fatalf("union = %v: want both declared ext edges and no local lib edge", union)
		}
	})
	t.Run("single-module default without a workspace file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"m/pb.yaml": {Data: []byte(modYAML("example.test/m", map[string]string{"example.test/dep": "v2.0.0"}))},
			"m/pb.lock": {Data: []byte("version: 1\nmodules: []\n")},
		}
		r, err := Load(fsys, "m")
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if r.File != nil || len(r.Modules) != 1 || r.Modules[0].Dir != "." {
			t.Fatalf("root = %+v, want the single-module default", r)
		}
	})
	t.Run("workspace using its own root keeps the root lockfile", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":     {Data: []byte("use:\n  - .\n  - lib\n")},
			"ws/pb.yaml":     {Data: []byte(modYAML("example.test/root", nil))},
			"ws/pb.lock":     {Data: []byte("version: 1\nmodules: []\n")},
			"ws/lib/pb.yaml": {Data: []byte(modYAML("example.test/lib", nil))},
		}
		if _, err := Load(fsys, "ws"); err != nil {
			t.Fatalf("Load = %v, want nil: the root lockfile IS the workspace lockfile", err)
		}
	})
	t.Run("rejected: use directory without a module file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":  {Data: []byte("use: [app]\n")},
			"ws/app/x.go": {Data: []byte("x")},
		}
		if _, err := Load(fsys, "ws"); err == nil ||
			!strings.Contains(err.Error(), "not a declared module root") {
			t.Fatalf("Load = %v, want module-root rejection", err)
		}
	})
	t.Run("rejected: invalid module file surfaces its own error", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":     {Data: []byte("use: [app]\n")},
			"ws/app/pb.yaml": {Data: []byte("module: not a module path !\n")},
		}
		if _, err := Load(fsys, "ws"); err == nil ||
			!strings.Contains(err.Error(), `module at "app"`) {
			t.Fatalf("Load = %v, want module-file rejection", err)
		}
	})
	t.Run("rejected: two directories declaring one module path", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":   {Data: []byte("use:\n  - a\n  - b\n")},
			"ws/a/pb.yaml": {Data: []byte(modYAML("example.test/same", nil))},
			"ws/b/pb.yaml": {Data: []byte(modYAML("example.test/same", nil))},
		}
		if _, err := Load(fsys, "ws"); err == nil ||
			!strings.Contains(err.Error(), "local override would be ambiguous") {
			t.Fatalf("Load = %v, want ambiguity rejection", err)
		}
	})
	t.Run("rejected: workspace module with its own lockfile", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":     {Data: []byte("use: [app]\n")},
			"ws/app/pb.yaml": {Data: []byte(modYAML("example.test/app", nil))},
			"ws/app/pb.lock": {Data: []byte("version: 1\nmodules: []\n")},
		}
		if _, err := Load(fsys, "ws"); err == nil ||
			!strings.Contains(err.Error(), "exactly one, at the root") {
			t.Fatalf("Load = %v, want lockfile rejection", err)
		}
	})
	t.Run("root directory is recorded cleaned", func(t *testing.T) {
		fsys := fstest.MapFS{
			"m/pb.yaml": {Data: []byte(modYAML("example.test/m", nil))},
		}
		r, err := Load(fsys, "m/.")
		if err != nil || r.Dir != "m" {
			t.Fatalf("Load = (%+v, %v), want the cleaned root m", r, err)
		}
	})
	t.Run("union order is a pure function of the declarations", func(t *testing.T) {
		// Build order differs from sorted order on both axes: bbb before
		// aaa across path order, and the higher ext version declared by
		// the earlier module. Deterministic construction plus the final
		// sort must yield exactly path-then-version order.
		fsys := fstest.MapFS{
			"ws/pb.work":   {Data: []byte("use:\n  - a\n  - b\n")},
			"ws/a/pb.yaml": {Data: []byte(modYAML("example.test/a", map[string]string{"ext.test/bbb": "v1.0.0", "ext.test/aaa": "v2.0.0"}))},
			"ws/b/pb.yaml": {Data: []byte(modYAML("example.test/b", map[string]string{"ext.test/aaa": "v1.0.0"}))},
		}
		r, err := Load(fsys, "ws")
		if err != nil {
			t.Fatal(err)
		}
		union, err := r.Requirements()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"ext.test/aaa@v1.0.0", "ext.test/aaa@v2.0.0", "ext.test/bbb@v1.0.0"}
		if len(union) != len(want) {
			t.Fatalf("union = %v, want %v", union, want)
		}
		for i, w := range want {
			if got := union[i].Path + "@" + union[i].Version.String(); got != w {
				t.Fatalf("union[%d] = %s, want %s (full order %v)", i, got, w, union)
			}
		}
	})
	t.Run("filesystem failures on the workspace file surface", func(t *testing.T) {
		base := fstest.MapFS{
			"ws/pb.work":     {Data: []byte("use: [app]\n")},
			"ws/app/pb.yaml": {Data: []byte(modYAML("example.test/app", nil))},
		}
		if _, err := Load(selectFS{base, "pb.work", errors.New("work io")}, "ws"); err == nil ||
			!strings.Contains(err.Error(), "work io") {
			t.Fatalf("Load = %v, want the workspace-file read error", err)
		}
	})
	t.Run("filesystem failures on a lockfile probe surface", func(t *testing.T) {
		base := fstest.MapFS{
			"ws/pb.work":     {Data: []byte("use: [app]\n")},
			"ws/app/pb.yaml": {Data: []byte(modYAML("example.test/app", nil))},
		}
		if _, err := Load(selectFS{base, "pb.lock", errors.New("lock io")}, "ws"); err == nil ||
			!strings.Contains(err.Error(), "lock io") {
			t.Fatalf("Load = %v, want the lockfile stat error", err)
		}
	})
}

// TestRequirementsInvalidVersionFailsClosed pins the defense-in-depth
// arm reachable only through a hand-constructed Root: module files
// parsed by Load carry validated versions, but Requirements must not
// silently drop or misorder a malformed one.
func TestRequirementsInvalidVersionFailsClosed(t *testing.T) {
	r := &Root{Modules: []Module{
		{Dir: ".", File: &modfile.File{Module: "example.test/a", Deps: map[string]string{"example.test/x": "v1.0.0"}}},
		{Dir: "b", File: &modfile.File{Module: "example.test/b", Deps: map[string]string{"example.test/x": "bogus"}}},
	}}
	if _, err := r.Requirements(); err == nil ||
		!strings.Contains(err.Error(), "requirement example.test/x@") {
		t.Fatalf("Requirements = %v, want version rejection", err)
	}
}

// TestWorkspaceLocalsNeverEnterExternalSelection proves the local
// override's decision surface as a for-all property
// (REQ-work-local-resolution, REQ-work-external-resolution): over
// generated workspaces, every workspace module path is local and absent
// from the external union regardless of what version any requirement
// declares, and every external requirement appears with the highest
// declared version.
func TestWorkspaceLocalsNeverEnterExternalSelection(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 4).Draw(rt, "modules")
		localPaths := make([]string, n)
		for i := range localPaths {
			localPaths[i] = fmt.Sprintf("example.test/m%d", i)
		}
		externals := []string{"ext.test/a", "ext.test/b", "ext.test/c"}
		versions := []string{"v0.1.0", "v1.0.0", "v1.5.0", "v2.0.0"}

		fsys := fstest.MapFS{}
		var use []string
		declared := map[string]bool{} // every declared external (path, version) edge
		for i, p := range localPaths {
			deps := map[string]string{}
			// Requirements on sibling locals carry arbitrary versions:
			// the override must hold regardless.
			for j, q := range localPaths {
				if j != i && rapid.Bool().Draw(rt, "depLocal") {
					deps[q] = rapid.SampledFrom(versions).Draw(rt, "lv")
				}
			}
			for _, e := range externals {
				if rapid.Bool().Draw(rt, "depExt") {
					v := rapid.SampledFrom(versions).Draw(rt, "ev")
					deps[e] = v
					declared[e+"@"+v] = true
				}
			}
			dir := fmt.Sprintf("m%d", i)
			use = append(use, dir)
			fsys[fmt.Sprintf("ws/%s/pb.yaml", dir)] = &fstest.MapFile{Data: []byte(modYAML(p, deps))}
		}
		fsys["ws/pb.work"] = &fstest.MapFile{Data: []byte("use:\n  - " + strings.Join(use, "\n  - ") + "\n")}

		r, err := Load(fsys, "ws")
		if err != nil {
			t.Fatalf("Load = %v", err)
		}
		union, err := r.Requirements()
		if err != nil {
			t.Fatalf("Requirements = %v", err)
		}
		for _, p := range localPaths {
			if _, ok := r.IsLocal(p); !ok {
				t.Fatalf("IsLocal(%s) = false: every workspace module is local", p)
			}
		}
		// The union is exactly the declared external edge set: every
		// declared (path, version) pair present once, locals absent
		// entirely — never a per-path collapse.
		got := map[string]bool{}
		for _, req := range union {
			key := req.Path + "@" + req.Version.String()
			if got[key] {
				t.Fatalf("duplicate edge %s in union", key)
			}
			got[key] = true
			for _, p := range localPaths {
				if req.Path == p {
					t.Fatalf("local path %s entered external selection", p)
				}
			}
		}
		if len(got) != len(declared) {
			t.Fatalf("union = %v, want exactly the declared edges %v", got, declared)
		}
		for e := range declared {
			if !got[e] {
				t.Fatalf("declared edge %s missing from union", e)
			}
		}
	})
}

// TestLockfilePlacementStructural proves REQ-work-lockfile at the data
// model: a Module carries a directory and a module file and nothing
// else — there is no field a per-module lockfile location could live
// in — and a Root carries exactly the root directory, the workspace
// file, and its modules, so the lockfile's home is derivable from the
// root alone. The behavioral half (Load rejecting a nested pb.lock) is
// TestLoad's lockfile subtest.
func TestLockfilePlacementStructural(t *testing.T) {
	structural.ExportedData[Module](t,
		structural.FieldOf[string]("Dir"),
		structural.FieldOf[*modfile.File]("File"),
	)
	// Root carries decision methods, so its field set is pinned by
	// reflection in this same analyzer-classified test.
	rt := reflect.TypeFor[Root]()
	if rt.NumField() != 3 || rt.Field(0).Name != "Dir" || rt.Field(1).Name != "File" || rt.Field(2).Name != "Modules" {
		t.Fatalf("Root fields changed: the lockfile's home must stay derivable from the root alone")
	}
}

func file(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

// LoadFor enforces membership (REQ-work-membership): a module inside a
// workspace that does not use it must not resolve — the governing root
// would neither treat it as local nor carry its requirements.
func TestLoadForMembership(t *testing.T) {
	fsys := fstest.MapFS{
		"pb.work":            file("use:\n  - a\n"),
		"a/pb.yaml":          file("module: example.com/a\n"),
		"a/deep/x.proto":     file(""),
		"stray/pb.yaml":      file("module: example.com/stray\n"),
		"stray/deep/y.proto": file(""),
		"docs/readme.md":     file(""),
		"solo/pb.yaml":       file("module: example.com/solo\n"),
	}

	t.Run("member module resolves", func(t *testing.T) {
		for _, dir := range []string{"a", "a/deep"} {
			root, err := LoadFor(fsys, dir)
			if err != nil {
				t.Fatalf("LoadFor(%s): %v", dir, err)
			}
			if root.Dir != "." || root.File == nil {
				t.Fatalf("LoadFor(%s) root = %+v", dir, root)
			}
		}
	})

	t.Run("unlisted module fails closed", func(t *testing.T) {
		for _, dir := range []string{"stray", "stray/deep"} {
			if _, err := LoadFor(fsys, dir); !errors.Is(err, ErrNotMember) {
				t.Fatalf("LoadFor(%s) err = %v, want ErrNotMember", dir, err)
			}
		}
	})

	t.Run("non-module directory operates against the workspace", func(t *testing.T) {
		root, err := LoadFor(fsys, "docs")
		if err != nil || root.File == nil {
			t.Fatalf("LoadFor(docs) = %+v, %v", root, err)
		}
	})

	t.Run("single-module default outside any workspace", func(t *testing.T) {
		solo := fstest.MapFS{
			"m/pb.yaml":      file("module: example.com/m\n"),
			"m/deep/x.proto": file(""),
		}
		root, err := LoadFor(solo, "m/deep")
		if err != nil || root.File != nil || root.Dir != "m" {
			t.Fatalf("LoadFor single-module = %+v, %v", root, err)
		}
	})

	t.Run("no root at all", func(t *testing.T) {
		if _, err := LoadFor(fstest.MapFS{"x.txt": file("")}, "."); !errors.Is(err, ErrNoRoot) {
			t.Fatalf("err = %v, want ErrNoRoot", err)
		}
	})
}

// LoadFor's remaining arms: nested roots exercise the relative-path
// computation, "." membership, load failures, walk containment, and
// seam errors.
func TestLoadForArms(t *testing.T) {
	t.Run("nested root: member and non-member relative paths", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":      file("use:\n  - a\n"),
			"ws/a/pb.yaml":    file("module: example.com/a\n"),
			"ws/b/pb.yaml":    file("module: example.com/b\n"),
			"ws/a/sub/x.txt":  file(""),
			"ws/b/deep/y.txt": file(""),
		}
		if _, err := LoadFor(fsys, "ws/a/sub"); err != nil {
			t.Fatalf("nested member: %v", err)
		}
		if _, err := LoadFor(fsys, "ws/b/deep"); !errors.Is(err, ErrNotMember) {
			t.Fatalf("nested non-member err = %v, want ErrNotMember", err)
		}
	})

	t.Run("workspace using its own root as a member", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work":     file("use:\n  - .\n"),
			"ws/pb.yaml":     file("module: example.com/root\n"),
			"ws/docs/x.txt":  file(""),
			"ws/deep/y.yaml": file(""),
		}
		for _, dir := range []string{"ws", "ws/docs", "ws/deep"} {
			if _, err := LoadFor(fsys, dir); err != nil {
				t.Fatalf("LoadFor(%s): %v", dir, err)
			}
		}
	})

	t.Run("load failure propagates", func(t *testing.T) {
		fsys := fstest.MapFS{
			"ws/pb.work": file("use:\n  - missing\n"),
			"ws/a/x.txt": file(""),
		}
		if _, err := LoadFor(fsys, "ws/a"); err == nil ||
			!strings.Contains(err.Error(), "not a declared module root") {
			t.Fatalf("err = %v, want the missing-member load failure", err)
		}
	})

	t.Run("module above the workspace root never decides membership", func(t *testing.T) {
		fsys := fstest.MapFS{
			"pb.yaml":       file("module: example.com/outer\n"),
			"ws/pb.work":    file("use:\n  - a\n"),
			"ws/a/pb.yaml":  file("module: example.com/a\n"),
			"ws/docs/x.txt": file(""),
		}
		// docs has no module of its own; the outer pb.yaml sits above
		// the workspace and must not be consulted.
		root, err := LoadFor(fsys, "ws/docs")
		if err != nil || root.Dir != "ws" {
			t.Fatalf("LoadFor(ws/docs) = %+v, %v", root, err)
		}
	})

	t.Run("find seam failure propagates", func(t *testing.T) {
		boom := errors.New("seam gone")
		base := fstest.MapFS{"ws/pb.work": file("use:\n  - a\n"), "ws/a/pb.yaml": file("module: example.com/a\n")}
		if _, err := LoadFor(errFS{base, boom}, "ws/a"); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the seam failure", err)
		}
	})
}
