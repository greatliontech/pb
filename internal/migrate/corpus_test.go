package migrate

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/module/version"
)

var update = flag.Bool("update", false, "rewrite the corpus goldens from the current run")

// corpusDiscovery answers every path with one release: the corpus
// exercises the mapping, never discovery, so a name the tables hold
// always resolves and an unmapped fact is the mapping's own.
type corpusDiscovery struct{}

func (corpusDiscovery) Versions(context.Context, string) ([]version.Version, error) {
	return nil, errors.New("the deps step asks for the latest, never the listing")
}

func (corpusDiscovery) Latest(context.Context, string) (version.Version, error) {
	return version.Parse("v1.0.0")
}

// corpusTags answers every plugin repository with the same tags, so a
// versionless plugin resolves to v1.2.0 whatever its name.
func corpusTags(context.Context, string) ([]string, error) {
	return []string{"latest", "v1.0.0", "v1.2.0", "sha256-ab.sig"}, nil
}

// The migration corpus (REQ-migrate-verb, REQ-migrate-report): each
// entry under testdata/corpus is one shape a buf configuration takes,
// written under example names, and its golden is the verb's whole
// report over it with the run's status last, the verb invoked with
// the flags the entry's migrate.flags lists, one per line, where it
// has one — the replacements a private name needs, the
// configuration's directory below the root. A golden whose status
// is unmapped records the shape's gap by the exact fact; the corpus
// is closed when every status reads mapped. A refusal is never a
// status a golden may record: every entry is a configuration buf
// accepts, so the verb refusing one is its defect, and the test
// fails on it.
func TestCorpus(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the corpus is empty")
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			dir := filepath.Join("testdata", "corpus", e.Name())
			ws := memfs.New()
			if err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || d.Name() == "report.golden" || d.Name() == "migrate.flags" {
					return err
				}
				rel, err := filepath.Rel(dir, p)
				if err != nil {
					return err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				return util.WriteFile(ws, filepath.ToSlash(filepath.Join("repo", rel)), b, 0o644)
			}); err != nil {
				t.Fatal(err)
			}
			inv := Invocation{
				WS: ws, Dir: "repo", ModulePath: "example.com/acme/" + e.Name(),
				Discovery: corpusDiscovery{}, PluginTags: corpusTags,
				Tidy: func(context.Context) error { return nil },
			}
			if flags, err := os.ReadFile(filepath.Join(dir, "migrate.flags")); err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(flags)), "\n") {
					flag, value, _ := strings.Cut(strings.TrimPrefix(line, "--"), " ")
					switch flag {
					case "dep", "plugin":
						if err := inv.Replacements.Replace(flag, value); err != nil {
							t.Fatal(err)
						}
					case "config":
						inv.Config = value
					default:
						t.Fatalf("migrate.flags: no flag --%s", flag)
					}
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			var out strings.Builder
			inv.Out = &out
			err := Run(context.Background(), inv)
			// Every entry is a configuration buf accepts, so a refusal is
			// the verb's defect, never a gap a golden may record.
			status := "mapped"
			switch {
			case errors.Is(err, ErrUnmapped):
				status = "unmapped"
			case err != nil:
				status = "failed: " + err.Error()
				t.Errorf("the verb refused a configuration buf accepts: %v", err)
			}
			got := []byte(out.String() + "-- status: " + status + "\n")
			golden := filepath.Join(dir, "report.golden")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("the report differs from %s (run with -update after reviewing):\n%s", golden, got)
			}
		})
	}
}

// The templates beside a configuration are listed by name and sorted,
// a directory bearing a template's name is none, and a v1 member's
// lock is read beside its buf.yaml (REQ-migrate-gen, the buf
// configuration term).
func TestReadSourceTemplatesAndMemberLocks(t *testing.T) {
	ws := memfs.New()
	for p, text := range map[string]string{
		"repo/buf.work.yaml":    "version: v1\ndirectories:\n  - api\n",
		"repo/api/buf.yaml":     "version: v1\n",
		"repo/api/buf.lock":     "version: v1\ndeps:\n  - remote: buf.build\n    owner: acme\n    repository: dep\n    commit: 00000000000000000000000000000002\nextra: x\n",
		"repo/buf.gen.web.yaml": "version: v2\nplugins:\n  - local: gen-web\n    out: gen/web\n",
		"repo/buf.gen.es.yaml":  "version: v2\nbogus: x\nplugins:\n  - local: gen-es\n    out: gen/es\n",
		"repo/buf.gen.x.yaml/f": "a directory bearing a template's name\n",
	} {
		if err := util.WriteFile(ws, p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, _, _, err := ReadSource(ws, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(src.Templates, ",") != "buf.gen.es.yaml,buf.gen.web.yaml" {
		t.Fatalf("templates = %v, want the two files sorted, the directory left", src.Templates)
	}
	if l := src.MemberLocks["api"]; l == nil || len(l.Deps) != 1 || l.Deps[0].Name != "buf.build/acme/dep" || len(l.Unmodeled) != 1 {
		t.Fatalf("member lock = %+v, want api's read with its one unmodeled key", src.MemberLocks)
	}
	// The member lock's unmodeled key is a fact of the run.
	var out strings.Builder
	err = Run(context.Background(), Invocation{WS: ws, Dir: "repo", ModulePath: "example.com/acme/w", Discovery: corpusDiscovery{}, PluginTags: corpusTags, Tidy: func(context.Context) error { return nil }, Out: &out})
	if !errors.Is(err, ErrUnmapped) || !strings.Contains(out.String(), "api/buf.lock extra !! a key the migration does not model\n") {
		t.Fatalf("Run = %v, report:\n%s", err, out.String())
	}
	// The templates are read as the generation file is, their
	// entries in name order, whether or not a buf.gen.yaml lies there.
	if !strings.Contains(out.String(), "buf.gen.es.yaml plugins[0].local gen-es -> local: gen-es\nbuf.gen.es.yaml plugins[0].out gen/es -> out: gen/es\nbuf.gen.web.yaml plugins[0].local gen-web -> local: gen-web\n") {
		t.Fatalf("templates' entries:\n%s", out.String())
	}
	// A lone template's top-level keys the reader passed over are
	// reported among the keys no step models, keyed by its name.
	if !strings.Contains(out.String(), "buf.gen.es.yaml bogus !! a key the migration does not model\n") || strings.Index(out.String(), "bogus") < strings.Index(out.String(), "gen-web -> local") {
		t.Fatalf("a template's top-level key:\n%s", out.String())
	}
	// One that does not parse fails the verb naming it.
	if err := util.WriteFile(ws, "repo/buf.gen.web.yaml", []byte("not: parsed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), Invocation{WS: ws, Dir: "repo", ModulePath: "example.com/acme/w", Discovery: corpusDiscovery{}, PluginTags: corpusTags, Tidy: func(context.Context) error { return nil }, Out: &strings.Builder{}})
	if err == nil || !strings.HasPrefix(err.Error(), "buf.gen.web.yaml: ") {
		t.Fatalf("a template that does not parse: %v", err)
	}
}
