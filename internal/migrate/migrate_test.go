package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

func facts(l *Layout) string { return factsOf(l.Facts) }

func factsOf(fs []Fact) string {
	var out []string
	for _, f := range fs {
		if f.Mapped {
			out = append(out, f.Source+" -> "+f.Text)
		} else {
			out = append(out, f.Source+" !! "+f.Text)
		}
	}
	return strings.Join(out, "\n")
}

// The modules a configuration declares become module files at their
// roots, the workspace file where several (REQ-migrate-modules).
func TestModules(t *testing.T) {
	v1 := `version: v1
name: buf.build/acme/petapis
build:
  excludes: [vendor]
`
	cfg, err := bufconfig.ParseFile([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Modules(&Source{File: cfg}, "github.com/acme/petapis")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Modules) != 1 || l.Modules["."].Module != "github.com/acme/petapis" || l.Workspace != nil {
		t.Fatalf("lone v1: %+v", l)
	}
	want := "buf.yaml . -> pb.yaml module: github.com/acme/petapis\n" +
		"buf.yaml.name buf.build/acme/petapis -> module: github.com/acme/petapis (a BSR name is no place pb fetches from)\n" +
		"buf.yaml.build.excludes vendor !! a pb module's file set is every regular file under its root"
	if got := facts(l); got != want {
		t.Fatalf("lone v1 facts:\n%s", got)
	}

	// A v1 workspace: each directory's own buf.yaml declares its module.
	work, err := bufconfig.ParseWork([]byte("version: v1\ndirectories:\n  - ./a\n  - b/c\n"))
	if err != nil {
		t.Fatal(err)
	}
	inA, err := bufconfig.ParseFile([]byte("version: v1\nname: buf.build/acme/a\nbuild:\n  excludes: [gen]\n"))
	if err != nil {
		t.Fatal(err)
	}
	inBC, err := bufconfig.ParseFile([]byte("version: v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := &Source{Work: work, Members: map[string]*bufconfig.File{"a": inA, "b/c": inBC}}
	l, err = Modules(src, "github.com/acme/mono")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Modules) != 2 || l.Modules["a"].Module != "github.com/acme/mono/a" || l.Modules["b/c"].Module != "github.com/acme/mono/b/c" || strings.Join(l.Workspace.Use, ",") != "a,b/c" {
		t.Fatalf("v1 workspace: %+v %+v", l.Modules, l.Workspace)
	}
	want = "buf.work.yaml directories[0] ./a -> a/pb.yaml module: github.com/acme/mono/a\n" +
		"a/buf.yaml.name buf.build/acme/a -> module: github.com/acme/mono/a (a BSR name is no place pb fetches from)\n" +
		"a/buf.yaml.build.excludes gen !! a pb module's file set is every regular file under its root\n" +
		"buf.work.yaml directories[1] b/c -> b/c/pb.yaml module: github.com/acme/mono/b/c"
	if got := facts(l); got != want {
		t.Fatalf("v1 workspace facts:\n%s", got)
	}
	// A directory with no buf.yaml is a module under buf's default
	// configuration: laid out, nothing to report of it.
	delete(src.Members, "b/c")
	if l, err := Modules(src, "github.com/acme/mono"); err != nil || l.Modules["b/c"].Module != "github.com/acme/mono/b/c" || len(l.Facts) != 4 {
		t.Fatalf("a directory with no buf.yaml: %+v %v", l, err)
	}
	v2member, err := bufconfig.ParseFile([]byte("version: v2\nmodules:\n  - path: .\n"))
	if err != nil {
		t.Fatal(err)
	}
	src.Members["b/c"] = v2member
	if _, err := Modules(src, "github.com/acme/mono"); err == nil || !strings.Contains(err.Error(), "b/c/buf.yaml is not a v1 buf.yaml") {
		t.Fatalf("a v2 member: %v", err)
	}

	v2 := `version: v2
modules:
  - path: proto/a
    name: buf.build/acme/a
    excludes: [proto/a/internal]
  - path: proto/b
    includes: [proto/b/pub]
`
	cfg, err = bufconfig.ParseFile([]byte(v2))
	if err != nil {
		t.Fatal(err)
	}
	l, err = Modules(&Source{File: cfg}, "github.com/acme/mono")
	if err != nil {
		t.Fatal(err)
	}
	if l.Modules["proto/a"].Module != "github.com/acme/mono/proto/a" || l.Modules["proto/b"].Module != "github.com/acme/mono/proto/b" || strings.Join(l.Workspace.Use, ",") != "proto/a,proto/b" {
		t.Fatalf("v2: %+v %+v", l.Modules, l.Workspace)
	}
	want = "buf.yaml modules[0] proto/a -> proto/a/pb.yaml module: github.com/acme/mono/proto/a\n" +
		"buf.yaml modules[0].name buf.build/acme/a -> module: github.com/acme/mono/proto/a (a BSR name is no place pb fetches from)\n" +
		"buf.yaml modules[0].excludes proto/a/internal !! a pb module's file set is every regular file under its root\n" +
		"buf.yaml modules[1] proto/b -> proto/b/pb.yaml module: github.com/acme/mono/proto/b\n" +
		"buf.yaml modules[1].includes proto/b/pub !! a pb module's file set is every regular file under its root"
	if got := facts(l); got != want {
		t.Fatalf("v2 facts:\n%s", got)
	}

	// A module whose directory holds another's is refused: pb has no
	// nested module (the directory itself holds every other).
	for _, in := range []string{"version: v2\nmodules:\n  - path: .\n  - path: sub\n", "version: v2\nmodules:\n  - path: proto/x\n  - path: proto\n"} {
		cfg, err := bufconfig.ParseFile([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Modules(&Source{File: cfg}, "github.com/acme/mono"); err == nil || !strings.Contains(err.Error(), "lies within module") || !strings.Contains(err.Error(), "never publishable") {
			t.Fatalf("nested: %v", err)
		}
	}

	// A lone v2 module at the directory: one file, no workspace; one
	// below it a workspace of one, the directory staying the root; one
	// below it: its path joined, no workspace.
	cfg, err = bufconfig.ParseFile([]byte("version: v2\nmodules:\n  - path: .\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Modules(&Source{File: cfg}, "github.com/acme/one"); err != nil || len(l.Modules) != 1 || l.Workspace != nil || l.Modules["."].Module != "github.com/acme/one" {
		t.Fatalf("lone v2: %+v %v", l, err)
	}
	cfg, err = bufconfig.ParseFile([]byte("version: v2\nmodules:\n  - path: ./proto\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Modules(&Source{File: cfg}, "github.com/acme/one"); err != nil || len(l.Modules) != 1 || l.Workspace == nil || strings.Join(l.Workspace.Use, ",") != "proto" || l.Modules["proto"].Module != "github.com/acme/one/proto" {
		t.Fatalf("lone v2 below the directory: %+v %v", l, err)
	}
	if _, err := Modules(nil, "github.com/acme/one"); err == nil {
		t.Fatal("no source: no error")
	}
	if _, err := Modules(&Source{}, "github.com/acme/one"); err == nil {
		t.Fatal("empty source: no error")
	}
	// A v2 buf.yaml beside a buf.work.yaml is two workspaces at once.
	if _, err := Modules(&Source{File: cfg, Work: work}, "github.com/acme/one"); err == nil || !strings.Contains(err.Error(), "two workspaces at once") {
		t.Fatalf("v2 beside a work file: %v", err)
	}

	for name, c := range map[string]struct {
		file, work, path, want string
		cfg                    *bufconfig.File // in place of file, for a value no parse yields
	}{
		"no path":     {file: v1, want: "no module path"},
		"bad path":    {file: v1, path: "acme", want: "module path"},
		"twice":       {cfg: &bufconfig.File{Version: "v2", Modules: []bufconfig.Module{{Path: "a"}, {Path: "./a"}}}, path: "github.com/x/y", want: "declared twice"},
		"escapes":     {cfg: &bufconfig.File{Version: "v2", Modules: []bufconfig.Module{{Path: "../a"}}}, path: "github.com/x/y", want: "escapes"},
		"bad segment": {file: "version: v2\nmodules:\n  - path: a\n  - path: \"b c\"\n", path: "github.com/x/y", want: "module path"},
		"newline":     {cfg: &bufconfig.File{Version: "v2", Modules: []bufconfig.Module{{Path: "a\nb"}}}, path: "github.com/x/y", want: "module path"},
	} {
		cfg := c.cfg
		if cfg == nil {
			var err error
			if cfg, err = bufconfig.ParseFile([]byte(c.file)); err != nil {
				t.Fatal(err)
			}
		}
		var work *bufconfig.Work
		if c.work != "" {
			var err error
			if work, err = bufconfig.ParseWork([]byte(c.work)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Modules(&Source{File: cfg, Work: work}, c.path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A remote's URL spells a module path: host and path, the scheme,
// user, and a trailing .git dropped (REQ-migrate-modules).
func TestSpellOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/acme/petapis.git":     "github.com/acme/petapis",
		"https://GitHub.com/acme/PetAPIs":         "github.com/acme/PetAPIs",
		"ssh://git@github.com/acme/petapis.git":   "github.com/acme/petapis",
		"git@github.com:acme/petapis.git":         "github.com/acme/petapis",
		"github.com:acme/petapis":                 "github.com/acme/petapis",
		"git://git.example.org/proto/apis.git/":   "git.example.org/proto/apis",
		"https://user:pw@gitlab.com/group/sub/r/": "gitlab.com/group/sub/r",
	} {
		got, err := SpellOrigin(raw)
		if err != nil || got != want {
			t.Errorf("%s: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"https://github.com:8443/acme/petapis",
		"https://github.com/acme/petapis?x=1",
		"https://github.com/acme/petapis#frag",
		"https://github.com/acme/pet%20apis",
		"https://github.com/acme/pet%2Fapis",
		"https://github.com/acme/r.git#",
		"file:///srv/git/petapis.git",
		"https://github.com/",
		"localhost:acme/petapis",
		"/srv/git/petapis",
		"https://github.com/acme/pet apis",
	} {
		if got, err := SpellOrigin(raw); err == nil {
			t.Errorf("%s: %q, no error", raw, got)
		}
	}
}

// The origin of the repository a directory lies in, joined with the
// directory's path within it, is the module path absent the flag
// (REQ-migrate-modules).
func TestOriginPath(t *testing.T) {
	dir := scratchtest.Dir(t)
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "proto", "apis")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OriginPath(dir); err == nil || !errors.Is(err, ErrNoModulePath) || !strings.Contains(err.Error(), "no origin remote") {
		t.Fatalf("no origin: %v", err)
	}
	// The first URL is the fetch URL; a push URL follows it.
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"git@github.com:acme/mono.git", "ssh://git@github.com/acme/other.git"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := OriginPath(dir); err != nil || got != "github.com/acme/mono" {
		t.Fatalf("root: %q %v", got, err)
	}
	if got, err := OriginPath(nested); err != nil || got != "github.com/acme/mono/proto/apis" {
		t.Fatalf("nested: %q %v", got, err)
	}
	if _, err := OriginPath(scratchtest.NoRepo(t)); err == nil || !errors.Is(err, ErrNoModulePath) {
		t.Fatalf("no repository: %v", err)
	}
	// A directory whose name no module path admits.
	bad := filepath.Join(dir, "a b")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OriginPath(bad); err == nil || !errors.Is(err, ErrNoModulePath) {
		t.Fatalf("bad directory name: %v", err)
	}
}
