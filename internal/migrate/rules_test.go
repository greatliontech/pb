package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
)

const rs = Ruleset + ":"

func parseFile(t *testing.T, text string) *bufconfig.File {
	t.Helper()
	f, err := bufconfig.ParseFile([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func rulesOf(t *testing.T, src *Source) (*Layout, string) {
	t.Helper()
	l, err := Modules(src, "github.com/acme/x")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := Rules(src, l)
	if err != nil {
		t.Fatal(err)
	}
	return l, factsOf(facts)
}

// The lint file over the ruleset (REQ-migrate-rules): a lone v1
// module's sections at the root, categories and ids as the ruleset's
// qualified tags and names, DEFAULT read as STANDARD, ignores over a
// path's subtree of the section's kind, ignore_only naming its rule
// or a category's rules, what the ruleset lacks unmapped; the
// rule-shaping options unmapped where set to anything but their
// default, the comment switch a mapped fact
// (REQ-migrate-rule-options, REQ-migrate-comments).
func TestRules(t *testing.T) {
	v1 := parseFile(t, `version: v1
lint:
  use: [DEFAULT, COMMENTS, FILE_LAYOUT]
  except: [ENUM_VALUE_PREFIX, PROTOVALIDATE]
  ignore: [vendor, ./gen/x.proto]
  ignore_only:
    ENUM_ZERO_VALUE_SUFFIX: [legacy]
    COMMENTS: [old]
    NOPE: [x]
  enum_zero_value_suffix: _NONE
  service_suffix: Service
  rpc_allow_same_request_response: true
  allow_comment_ignores: true
breaking:
  use: [WIRE_JSON]
  ignore_unstable_packages: false
`)
	l, got := rulesOf(t, &Source{File: v1})
	comments := []string{"COMMENT_ENUM", "COMMENT_ENUM_VALUE", "COMMENT_FIELD", "COMMENT_MESSAGE", "COMMENT_ONEOF", "COMMENT_RPC", "COMMENT_SERVICE"}
	for i := range comments {
		comments[i] = rs + comments[i]
	}
	want := strings.Join([]string{
		"buf.yaml.lint.use DEFAULT -> enable: " + rs + "STANDARD (buf's DEFAULT is STANDARD)",
		"buf.yaml.lint.use COMMENTS -> enable: " + rs + "COMMENTS",
		"buf.yaml.lint.use FILE_LAYOUT !! no lint rule or category of the ruleset " + Ruleset,
		"buf.yaml.lint.except ENUM_VALUE_PREFIX -> exclude: " + rs + "ENUM_VALUE_PREFIX",
		"buf.yaml.lint.except PROTOVALIDATE !! no lint rule or category of the ruleset " + Ruleset,
		"buf.yaml.lint.enum_zero_value_suffix _NONE !! reshapes " + rs + "ENUM_ZERO_VALUE_SUFFIX, which checks _UNSPECIFIED: a pb rule has no parameters, so the option's meaning lives in a rule of one's own",
		"buf.yaml.lint.rpc_allow_same_request_response true !! reshapes " + rs + "RPC_REQUEST_RESPONSE_UNIQUE: a pb rule has no parameters, so the option's meaning lives in a rule of one's own",
		"buf.yaml.lint.allow_comment_ignores true -> the module's suppression comments are rewritten to pb:ignore",
		"buf.yaml.breaking.use WIRE_JSON -> enable: " + rs + "WIRE_JSON",
		"buf.yaml.lint.ignore_only.NOPE x !! no lint rule or category of the ruleset " + Ruleset,
		"buf.yaml.lint.ignore vendor -> ignore paths [vendor/**] kind lint",
		"buf.yaml.lint.ignore ./gen/x.proto -> ignore paths [gen/x.proto/**] kind lint",
		"buf.yaml.lint.ignore_only.COMMENTS old -> ignore paths [old/**] rules [" + strings.Join(comments, ", ") + "]",
		"buf.yaml.lint.ignore_only.ENUM_ZERO_VALUE_SUFFIX legacy -> ignore paths [legacy/**] rules [" + rs + "ENUM_ZERO_VALUE_SUFFIX]",
	}, "\n")
	if got != want {
		t.Fatalf("lone v1 facts:\n%s", got)
	}
	if !l.CommentIgnores["."] {
		t.Fatal("v1 with allow_comment_ignores true: comments not honored")
	}
	out, err := lintfile.Encode(l.Lint)
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "rulesets:\n  - " + Ruleset + "\nenable:\n  - " + rs + "COMMENTS\n  - " + rs + "STANDARD\n  - " + rs + "WIRE_JSON\nexclude:\n  - " + rs + "ENUM_VALUE_PREFIX\nignore:\n" +
		"  - paths:\n      - gen/x.proto/**\n    kind: lint\n" +
		"  - paths:\n      - legacy/**\n    rules:\n      - " + rs + "ENUM_ZERO_VALUE_SUFFIX\n" +
		"  - paths:\n      - old/**\n    rules:\n"
	for _, c := range comments {
		wantFile += "      - " + c + "\n"
	}
	wantFile += "  - paths:\n      - vendor/**\n    kind: lint\n"
	if string(out) != wantFile {
		t.Fatalf("lone v1 file:\n%s", out)
	}
	// The subtree glob matches the path itself and everything under it.
	if g := l.Lint.Ignore[0].Paths[0]; !g.Match("vendor") || !g.Match("vendor/a/b.proto") || g.Match("vendors") {
		t.Fatalf("the subtree glob: %s", g)
	}

	// No sections at all: buf's defaults, reported as such, the file
	// written all the same (REQ-migrate-verb); a v1 module without the
	// comment switch had no comment honored, a fact all the same.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\n")})
	if got != "buf.yaml.lint -> enable: "+rs+"STANDARD (buf's default, no lint section)\nbuf.yaml.breaking -> enable: "+rs+"FILE (buf's default, no breaking section)" || strings.Join(l.Lint.Enable, ",") != rs+"FILE,"+rs+"STANDARD" || len(l.Lint.Modules) != 0 || !l.CommentIgnores["."] {
		t.Fatalf("no sections: %s %+v %v", got, l.Lint, l.CommentIgnores)
	}
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v1\n")})
	if !strings.Contains(got, "buf.yaml.lint -> the module's suppression comments are left as they are: buf honored none without allow_comment_ignores") || l.CommentIgnores["."] {
		t.Fatalf("v1 without the switch: %s %v", got, l.CommentIgnores)
	}
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\nlint:\n  disallow_comment_ignores: true\n")})
	if !strings.Contains(got, "buf.yaml.lint.disallow_comment_ignores true -> the module's suppression comments are left as they are: buf honored none") || l.CommentIgnores["."] {
		t.Fatalf("v2 disallowing: %s %v", got, l.CommentIgnores)
	}
	// except is read where use is absent: buf's default less it.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v1\nlint:\n  except: [ENUM_VALUE_PREFIX]\n")})
	if !strings.Contains(got, "buf.yaml.lint.except ENUM_VALUE_PREFIX -> exclude: "+rs+"ENUM_VALUE_PREFIX") || strings.Join(l.Lint.Exclude, ",") != rs+"ENUM_VALUE_PREFIX" {
		t.Fatalf("except without use: %s %+v", got, l.Lint)
	}
	// An ignore of the module's directory disables the kind, the
	// section's use then read for nothing; both kinds disabled enable
	// nothing, spelled `[]`, never every rule.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v1\nlint:\n  use: [COMMENTS]\n  ignore: [., vendor]\nbreaking:\n  ignore_only:\n    FILE: [.]\n")})
	if strings.Contains(got, "COMMENTS") || strings.Contains(got, "vendor") || !strings.Contains(got, "buf.yaml.lint.ignore . -> lint disabled for .: no lint rule enabled for it (the ignore is the module's directory, which buf reads as disabling the kind)") || strings.Join(l.Lint.Enable, ",") != rs+"FILE" {
		t.Fatalf("disabled lint: %s %+v", got, l.Lint)
	}
	if len(l.Lint.Ignore) != 1 || l.Lint.Ignore[0].Paths[0].String() != "**" || len(l.Lint.Ignore[0].Rules) != len(rulesetTagged[check.KindBreaking]["FILE"]) {
		t.Fatalf("the module's directory under ignore_only: %+v", l.Lint.Ignore)
	}
	l, _ = rulesOf(t, &Source{File: parseFile(t, "version: v1\nlint:\n  ignore: [.]\nbreaking:\n  ignore: [.]\n")})
	if out, err := lintfile.Encode(l.Lint); err != nil || l.Lint.Enable == nil || len(l.Lint.Enable) != 0 || !strings.Contains(string(out), "\nenable: []\n") {
		t.Fatalf("both kinds disabled: %v %q", err, out)
	}

	// A v2 workspace: the top-level sections at the root, its ignores
	// each in the module holding the path (one in none unmapped), a
	// module's own section its entry with its whole selection, a
	// module disabled by an ignore of its directory its entry without
	// the kind.
	v2 := parseFile(t, `version: v2
modules:
  - path: proto/a
  - path: proto/b
    lint:
      use: [BASIC]
      ignore: [proto/b/legacy]
  - path: proto/c
lint:
  use: [STANDARD]
  ignore: [proto/a/gen, proto/c, elsewhere]
breaking:
  ignore_only:
    FIELD_SAME_TYPE: [proto/a/old.proto]
`)
	l, got = rulesOf(t, &Source{File: v2})
	want = strings.Join([]string{
		"buf.yaml.lint.use STANDARD -> enable: " + rs + "STANDARD",
		"buf.yaml.breaking -> enable: " + rs + "FILE (buf's default, no use)",
		"buf.yaml.lint.ignore elsewhere !! lies in no module: buf skips it",
		"buf.yaml.lint.ignore proto/a/gen -> modules.proto/a.ignore paths [gen/**] kind lint",
		"buf.yaml.breaking.ignore_only.FIELD_SAME_TYPE proto/a/old.proto -> modules.proto/a.ignore paths [old.proto/**] rules [" + rs + "FIELD_SAME_TYPE]",
		"buf.yaml modules[1].lint.ignore proto/b/legacy -> modules.proto/b.ignore paths [legacy/**] kind lint",
		"buf.yaml modules[1].lint.use BASIC -> enable: " + rs + "BASIC",
		"buf.yaml.lint.ignore proto/c -> lint disabled for proto/c: no lint rule enabled for it (the ignore is the module's directory, which buf reads as disabling the kind)",
	}, "\n")
	if got != want {
		t.Fatalf("v2 facts:\n%s", got)
	}
	out, err = lintfile.Encode(l.Lint)
	if err != nil {
		t.Fatal(err)
	}
	wantFile = "rulesets:\n  - " + Ruleset + "\nenable:\n  - " + rs + "FILE\n  - " + rs + "STANDARD\nmodules:\n" +
		"  proto/a:\n    enable:\n      - " + rs + "FILE\n      - " + rs + "STANDARD\n    ignore:\n      - paths:\n          - gen/**\n        kind: lint\n      - paths:\n          - old.proto/**\n        rules:\n          - " + rs + "FIELD_SAME_TYPE\n" +
		"  proto/b:\n    enable:\n      - " + rs + "BASIC\n      - " + rs + "FILE\n    ignore:\n      - paths:\n          - legacy/**\n        kind: lint\n" +
		"  proto/c:\n    enable:\n      - " + rs + "FILE\n"
	if string(out) != wantFile {
		t.Fatalf("v2 file:\n%s", out)
	}
	// A module whose own section lands it on the root's selection with
	// no ignores gets no entry; a v2 module's section saying nothing
	// is no section of its own, as buf reads it, the top-level's
	// standing in, its paths included.
	l, _ = rulesOf(t, &Source{File: parseFile(t, "version: v2\nmodules:\n  - path: a\n    lint:\n      use: [STANDARD]\n  - path: b\n")})
	if len(l.Lint.Modules) != 0 || strings.Join(l.Lint.Enable, ",") != rs+"FILE,"+rs+"STANDARD" {
		t.Fatalf("an entry equal to the root: %+v", l.Lint)
	}
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\nmodules:\n  - path: a\n    lint: {}\n  - path: b\nlint:\n  use: [MINIMAL]\n  ignore: [a/gen]\n")})
	if !strings.Contains(got, "buf.yaml.lint.ignore a/gen -> modules.a.ignore paths [gen/**] kind lint") || strings.Join(l.Lint.Modules["a"].Enable, ",") != rs+"FILE,"+rs+"MINIMAL" {
		t.Fatalf("an empty own section: %s %+v", got, l.Lint.Modules)
	}
	// A v2 module's section spelling its options at their zero values
	// says nothing, as buf reads it by value: the file's section
	// governs it, its paths included.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\nmodules:\n  - path: a\n    lint:\n      rpc_allow_same_request_response: false\n      service_suffix: \"\"\n  - path: b\nlint:\n  use: [MINIMAL]\n  ignore: [a/gen]\n")})
	if !strings.Contains(got, "buf.yaml.lint.ignore a/gen -> modules.a.ignore paths [gen/**] kind lint") || strings.Join(l.Lint.Modules["a"].Enable, ",") != rs+"FILE,"+rs+"MINIMAL" {
		t.Fatalf("an own section at its zero values: %s %+v", got, l.Lint.Modules)
	}
	// A suffix spelled empty is buf's default too, no fact.
	if _, got := rulesOf(t, &Source{File: parseFile(t, "version: v2\nlint:\n  use: [BASIC]\n  service_suffix: \"\"\n  enum_zero_value_suffix: _UNSPECIFIED\n  rpc_allow_same_request_response: off\n")}); strings.Contains(got, "suffix") || strings.Contains(got, "rpc_allow") {
		t.Fatalf("a suffix at its default, a boolean spelled off: %s", got)
	}
	// An ignore_only naming a set rule reaches nothing under the root
	// selection; a package rule's finding sits at the package's first
	// file there; in a module's entry either is reached by the
	// module's directory alone.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v1\nlint:\n  ignore_only:\n    PACKAGE_NO_IMPORT_CYCLE: [legacy]\n    PACKAGE_SAME_DIRECTORY: [legacy]\n")})
	if !strings.Contains(got, "buf.yaml.lint.ignore_only.PACKAGE_NO_IMPORT_CYCLE legacy !! "+rs+"PACKAGE_NO_IMPORT_CYCLE is a set rule, whose finding has no path under the root selection: no ignore reaches it") || !strings.Contains(got, "buf.yaml.lint.ignore_only.PACKAGE_SAME_DIRECTORY legacy -> ignore paths [legacy/**] rules ["+rs+"PACKAGE_SAME_DIRECTORY]") || len(l.Lint.Ignore) != 1 {
		t.Fatalf("a set rule under the root: %s %+v", got, l.Lint.Ignore)
	}
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\nmodules:\n  - path: a\n    lint:\n      ignore_only:\n        PACKAGE_SAME_DIRECTORY: [a/legacy, a]\n  - path: b\n")})
	if !strings.Contains(got, "buf.yaml modules[0].lint.ignore_only.PACKAGE_SAME_DIRECTORY a/legacy !! "+rs+"PACKAGE_SAME_DIRECTORY's finding is located at the module's directory, which a path reaches not: ignore the module's directory to reach it") || !strings.Contains(got, "buf.yaml modules[0].lint.ignore_only.PACKAGE_SAME_DIRECTORY a -> modules.a.ignore paths [**] rules ["+rs+"PACKAGE_SAME_DIRECTORY]") || len(l.Lint.Modules["a"].Ignore) != 1 {
		t.Fatalf("a package rule in an entry: %s %+v", got, l.Lint.Modules)
	}
	// A lone v2 module at a directory: paths relative to the file, made
	// module-relative at the root; its own lint section governing, the
	// top-level lint's default and its ignores within the module are
	// no facts, its ignore in no module one all the same.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\nmodules:\n  - path: proto\n    lint:\n      use: [BASIC]\nlint:\n  ignore: [proto/vendor, other]\nbreaking:\n  ignore: [proto/old]\n")})
	if strings.Contains(got, "STANDARD") || !strings.Contains(got, "buf.yaml.breaking.ignore proto/old -> ignore paths [old/**] kind breaking") || strings.Contains(got, "proto/vendor") || !strings.Contains(got, "buf.yaml.lint.ignore other !! lies in no module: buf skips it") || len(l.Lint.Ignore) != 1 || len(l.Lint.Modules) != 0 || strings.Join(l.Lint.Enable, ",") != rs+"BASIC,"+rs+"FILE" {
		t.Fatalf("lone v2 at a directory: %s %+v", got, l.Lint)
	}
	// A shared ignore of the module's directory after another within
	// it: the kind disabled, the other reported by nobody, the entry
	// without ignores; a shared ignore_only id the ruleset lacks is
	// reported once whatever the modules' own sections.
	l, got = rulesOf(t, &Source{File: parseFile(t, "version: v2\nmodules:\n  - path: a\n    lint:\n      use: [BASIC]\n  - path: b\nlint:\n  ignore: [b/gen, b]\n  ignore_only:\n    NOPE: [b/x]\n")})
	if strings.Contains(got, "gen/**") || !strings.Contains(got, "lint disabled for b") || !strings.Contains(got, "buf.yaml.lint.ignore_only.NOPE b/x !! no lint rule or category") || len(l.Lint.Modules["b"].Ignore) != 0 || strings.Join(l.Lint.Modules["b"].Enable, ",") != rs+"FILE" {
		t.Fatalf("disabled after an ignore within: %s %+v", got, l.Lint.Modules)
	}

	// A v1 workspace: buf's v1 default at the root, each directory's
	// own file its entry where it differs, a directory with no file
	// under the default, reported as such; a buf.yaml beside the
	// workspace file is read for nothing, as buf leaves it.
	work, err := bufconfig.ParseWork([]byte("version: v1\ndirectories: [a, b]\n"))
	if err != nil {
		t.Fatal(err)
	}
	l, got = rulesOf(t, &Source{Work: work, File: parseFile(t, "version: v1\nlint:\n  use: [MINIMAL]\n"), Members: map[string]*bufconfig.File{"a": parseFile(t, "version: v1\nlint:\n  use: [MINIMAL]\n  ignore: [gen]\n  allow_comment_ignores: true\n")}})
	want = strings.Join([]string{
		"buf.yaml.lint -> nothing: beside buf.work.yaml, buf reads the directories' files alone",
		"a/buf.yaml.lint.ignore gen -> modules.a.ignore paths [gen/**] kind lint",
		"a/buf.yaml.lint.allow_comment_ignores true -> the module's suppression comments are rewritten to pb:ignore",
		"a/buf.yaml.lint.use MINIMAL -> enable: " + rs + "MINIMAL",
		"a/buf.yaml.breaking -> enable: " + rs + "FILE (buf's default, no breaking section)",
		"b/buf.yaml.lint -> the module's suppression comments are left as they are: buf honored none without allow_comment_ignores",
		"b/buf.yaml.lint -> enable: " + rs + "STANDARD (buf's default, no lint section)",
		"b/buf.yaml.breaking -> enable: " + rs + "FILE (buf's default, no breaking section)",
	}, "\n")
	if got != want {
		t.Fatalf("v1 workspace facts:\n%s", got)
	}
	if len(l.Lint.Modules) != 1 || strings.Join(l.Lint.Modules["a"].Enable, ",") != rs+"FILE,"+rs+"MINIMAL" || len(l.Lint.Modules["a"].Ignore) != 1 || strings.Join(l.Lint.Enable, ",") != rs+"FILE,"+rs+"STANDARD" || !l.CommentIgnores["a"] || l.CommentIgnores["b"] {
		t.Fatalf("v1 workspace file: %+v %v", l.Lint, l.CommentIgnores)
	}

	// What buf refuses: a module's own ignore outside the module, a
	// path escaping the configuration.
	for name, text := range map[string]string{
		"outside":  "version: v2\nmodules:\n  - path: a\n    lint:\n      ignore: [b/x]\n  - path: b\n",
		"escaping": "version: v1\nlint:\n  ignore: [../x]\n",
		"only out": "version: v2\nmodules:\n  - path: a\n    breaking:\n      ignore_only:\n        FILE: [b]\n  - path: b\n",
	} {
		src := &Source{File: parseFile(t, text)}
		l, err := Modules(src, "github.com/acme/x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Rules(src, l); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Rules(nil, nil); err == nil {
		t.Fatal("no source: no error")
	}
	// The declarations' checks hold for Rules as for Modules.
	if _, err := Rules(&Source{File: &bufconfig.File{Version: "v2", Modules: []bufconfig.Module{{Path: "a"}, {Path: "./a"}}}}, &Layout{}); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("a directory declared twice: %v", err)
	}
}

// The ruleset table is the ruleset's own files, id for id, kind for
// kind, tag for tag: held against a checkout of the ruleset where
// PB_BUF_RULES names one. Every rule an option reshapes is a rule of
// the ruleset, always.
func TestRulesetTable(t *testing.T) {
	for opt, o := range ruleOptions {
		if _, ok := rulesetRules[o.rule]; !ok && !strings.Contains(o.rule, " ") {
			t.Errorf("%s reshapes %s, which the ruleset lacks", opt, o.rule)
		}
	}
	root := os.Getenv("PB_BUF_RULES")
	if root == "" {
		t.Skip("PB_BUF_RULES names no checkout of the ruleset")
	}
	files := map[string][]byte{}
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".rules.yaml") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	located, err := rules.Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range located {
		for _, r := range l.File.Rules {
			seen[r.ID] = true
			want := rulesetRule{kind: r.Kind, target: r.Target, tags: strings.Join(r.Tags, " ")}
			if got := rulesetRules[r.ID]; got != want {
				t.Errorf("%s: table %+v, ruleset %+v", r.ID, got, want)
			}
		}
	}
	for id := range rulesetRules {
		if !seen[id] {
			t.Errorf("%s: in the table, not in the ruleset", id)
		}
	}
}
