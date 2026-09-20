// Package catalog is the engine's fixture corpus: buf's lint and
// breaking rules rewritten in CEL under environment 1, each rule
// compiled — proving the environment closed over the catalog — and
// evaluated over a schema built to trip every one of them, the
// findings held against a golden file. pb ships no rules; the corpus
// is a test.
package catalog

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/env1"
	"github.com/greatliontech/pb/internal/check/eval"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/testing/prototest"
)

var update = flag.Bool("update", false, "rewrite the golden findings from the current run")

// catalog reads the rule files under testdata/buf/<kind>.
func catalog(t *testing.T, kind string) []rules.Rule {
	t.Helper()
	files := map[string][]byte{}
	for p, b := range tree(t, filepath.Join("testdata", "buf", kind)) {
		if module.IsRuleFile(p) {
			files[p] = b
		}
	}
	located, err := rules.Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := lintfile.Select(&lintfile.File{}, []lintfile.Ruleset{{Path: "buf/" + kind, Files: located}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatalf("no rules under testdata/buf/%s", kind)
	}
	return rs
}

// sources reads a fixture directory's protobuf files by path.
func sources(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p, b := range tree(t, dir) {
		if strings.HasSuffix(p, ".proto") {
			out[p] = string(b)
		}
	}
	return out
}

// tree reads the regular files under dir by slash path relative to it.
func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(strings.TrimPrefix(p, dir+string(filepath.Separator)))] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func paths(srcs map[string]string) []string {
	out := make([]string, 0, len(srcs))
	for p := range srcs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// golden holds the run's findings against testdata/<name>.txt: every
// rule of the catalog fires at least once, and the lines are the
// golden's.
func golden(t *testing.T, name string, rs []rules.Rule, findings []check.Finding) {
	t.Helper()
	check.Sort(findings)
	var buf bytes.Buffer
	fired := map[string]bool{}
	for _, f := range findings {
		buf.WriteString(f.String() + "\n")
		fired[f.RuleID] = true
	}
	for _, r := range rs {
		if !fired[r.ID] {
			t.Errorf("%s never fires over the fixture", r.ID)
		}
	}
	path := filepath.Join("testdata", name+".txt")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("findings differ from %s (run with -update after reviewing):\n%s", path, buf.String())
	}
}

// Every lint rule of buf's catalog compiles under environment 1 and
// fires over the lint fixture as the golden says.
func TestLintCatalog(t *testing.T) {
	rs := catalog(t, "lint")
	srcs := sources(t, filepath.Join("testdata", "lint"))
	set := env1.NewSet(prototest.Compile(t, srcs))
	env, err := env1.New(set, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if _, err := env.Compile(r); err != nil {
			t.Errorf("%v", err)
		}
	}
	report, err := eval.Lint(env, paths(srcs), prototest.Source(srcs), rs)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "lint", rs, report.Findings)
}

// Every breaking rule of buf's catalog compiles under environment 1
// and fires over the old and new fixtures as the golden says.
func TestBreakingCatalog(t *testing.T) {
	rs := catalog(t, "breaking")
	oldSrc := sources(t, filepath.Join("testdata", "breaking", "old"))
	newSrc := sources(t, filepath.Join("testdata", "breaking", "new"))
	oldSet := env1.NewSet(prototest.Compile(t, oldSrc))
	newSet := env1.NewSet(prototest.Compile(t, newSrc))
	env, err := env1.New(newSet, oldSet)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if _, err := env.Compile(r); err != nil {
			t.Errorf("%v", err)
		}
	}
	report, err := eval.Breaking(env, paths(oldSrc), paths(newSrc), prototest.Source(newSrc), prototest.Source(oldSrc), rs)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "breaking", rs, report.Findings)
}
