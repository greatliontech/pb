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
// report over it with the run's status last. A golden whose status is
// unmapped records the shape's gap by the exact fact; the corpus is
// closed when every status reads mapped. A refusal is never a status
// a golden may record: every entry is a configuration buf accepts,
// so the verb refusing one is its defect, and the test fails on it.
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
				if err != nil || d.IsDir() || d.Name() == "report.golden" {
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
			var out strings.Builder
			err := Run(context.Background(), Invocation{
				WS: ws, Dir: "repo", ModulePath: "example.com/acme/" + e.Name(),
				Discovery: corpusDiscovery{}, PluginTags: corpusTags,
				Tidy: func(context.Context) error { return nil },
				Out:  &out,
			})
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
		"repo/buf.gen.web.yaml": "not: parsed\n",
		"repo/buf.gen.es.yaml":  "not: parsed\n",
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
}
