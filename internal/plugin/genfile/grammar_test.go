package genfile

import (
	"strings"
	"testing"
)

// Every grammar's accept/reject set at its character-range edges: the
// characters adjacent to each range ('`' before 'a', '{' after 'z',
// '/' before '0', ':' after '9', '@' before 'A', '[' after 'Z') and the
// separator rules at their length limits.
func TestGrammarEdges(t *testing.T) {
	type tc struct {
		ok  []string
		bad []string
	}
	suites := map[string]struct {
		check func(string) error
		tc
	}{
		"label": {checkLabel, tc{
			ok:  []string{"a", "z", "0", "9", "a-b", "a0-9z", "a-b-c"},
			bad: []string{"", "-a", "a-", "a--b", "A", "a_b", "a.b", "a`", "a{", "a/", "a:", "a@", "a[", "`", "{"},
		}},
		"component": {checkRepoComponent, tc{
			ok:  []string{"a", "z0", "a.b", "a-b", "a--b", "a---b", "a_b", "a__b", "a9", "9a"},
			bad: []string{"", ".a", "a.", "a..b", "a-.b", "a._b", "a___b", "a-_b", "a_-b", "A", "a/b", "a`b", "a{b", "a:b", "a@b", "a[b", "-a", "a-", "_a", "a_"},
		}},
		"tag": {checkTag, tc{
			ok:  []string{"a", "z", "A", "Z", "0", "9", "_", "a.b-c_d", "v1.2.3-rc.1", strings.Repeat("a", 128), "_" + strings.Repeat("z", 127)},
			bad: []string{"", ".a", "-a", "a+b", "a b", "a/b", "a`", "a{", "a@", "a[", "a:", "`", "{", "@", "[", "/", ":", strings.Repeat("a", 129)},
		}},
		"registry": {checkRegistry, tc{
			ok:  []string{"localhost", "a.b", "a.b:1", "a.b:65535", "1.2.3.4", "localhost:5000", "a-b.c", "myreg:5000"},
			bad: []string{"", "a", "a:0", "a:65536", "a:x", "a:", ":5", "a..b", ".a", "a.", "A.b", "a.b:1:2", "a_b.c", "a.b:00000", "a.b:-1", "a.b:.5", "a.b:1.0"},
		}},
		"option": {checkOptionName, tc{
			ok:  []string{"a", "z", "A", "Z", "_", "a0", "a9", "a_b", "a.b", "a.b.c", "(a)", "(a.b)", "(a.b).c", "(a.b).c.d"},
			bad: []string{"", "9a", "a-b", "a`", "a{", "a[", "a@", "a/", "a:", "(", ")", "(a", "a)", "(a)b", "(a)bc", "(a..b)", "()", "a.", ".a", "a..b", "(a.b).", "a.9", "a./", "a.(b)", "(a).(b)"},
		}},
		"out": {CheckOut, tc{
			ok:  []string{"a", "a/b", "..a", "a..", ".", "gen/go", "..."},
			bad: []string{"", "/", "/a", "..", "../a", "../..", "a/", "./a", "a//b", "a\\b", "a/./b", "a/../b"},
		}},
		"local": {checkLocal, tc{
			ok:  []string{"p", "./p", "/abs/p", "a/b", "../tool", "./a/b", "..p"},
			bad: []string{"", ".", "..", "./", "a//b", "a\\b", "./.", "././p", "a/", "/"},
		}},
	}
	for name, suite := range suites {
		for _, in := range suite.ok {
			if err := suite.check(in); err != nil {
				t.Errorf("%s: %q rejected: %v", name, in, err)
			}
		}
		for _, in := range suite.bad {
			if err := suite.check(in); err == nil {
				t.Errorf("%s: %q accepted", name, in)
			}
		}
	}
}

// ReferenceRepository strips exactly the tag, keeping registry ports
// and nested repositories.
func TestReferenceRepository(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/o/p:v1":        "ghcr.io/o/p",
		"localhost:5000/a/b:v2": "localhost:5000/a/b",
		"r.io:443/x:latest":     "r.io:443/x",
	} {
		if got := ReferenceRepository(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// SplitOption's exact tuples for every navigable form — success and
// error alike — so no component can swap or leak.
func TestSplitOptionTuples(t *testing.T) {
	cases := []struct{ in, ext, field, builtin, wantErr string }{
		{"go_package", "", "", "go_package", ""},
		{"a.b.c", "", "", "a.b.c", ""},
		{"(a.b)", "a.b", "", "", ""},
		{"(a)", "a", "", "", ""},
		{"(a.b).c", "a.b", "c", "", ""},
		{"(a.b).c.d", "a.b", "c.d", "", ""},
		{"", "", "", "", "empty"},
		{"(a", "", "", "", "unclosed extension name"},
		{"(a..b)", "", "", "", "invalid extension name"},
		{"(a)b", "", "", "", "not a protobuf option name"},
		{"(a)bc", "", "", "", "not a protobuf option name"},
		{"(a).", "", "", "", "not a protobuf option name"},
		{"9a", "", "", "", "not a protobuf option name"},
		{"(a).9b", "", "", "", "not a protobuf option name"},
	}
	for _, tc := range cases {
		ext, field, builtin, err := SplitOption(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || ext != "" || field != "" || builtin != "" {
				t.Errorf("%q: (%q,%q,%q,%v), want error %q with zero tuple", tc.in, ext, field, builtin, err, tc.wantErr)
			}
			continue
		}
		if err != nil || ext != tc.ext || field != tc.field || builtin != tc.builtin {
			t.Errorf("%q: (%q,%q,%q,%v), want (%q,%q,%q)", tc.in, ext, field, builtin, err, tc.ext, tc.field, tc.builtin)
		}
	}
}

// Reference-level composition edges: empty repository, empty tag,
// tag-vs-port, colon inside a component.
func TestReferenceCompositionEdges(t *testing.T) {
	for _, bad := range []string{"ghcr.io/:v1", "ghcr.io/p:", "h.io/p:v1:v2", "h.io/a:b/c:v1", "h.io", "h.io/", "/p:v1", "localhost:5000/p"} {
		if err := CheckReference(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := CheckReference("localhost:5000/p:v1"); err != nil {
		t.Errorf("port and tag together: %v", err)
	}
	// The empty-remainder forms are registry errors, not tag errors.
	for _, in := range []string{"h.io/", "h.io", "protoc-gen-go:v1"} {
		if err := CheckReference(in); err == nil || !strings.Contains(err.Error(), "has no registry") {
			t.Errorf("%q: err = %v, want no-registry", in, err)
		}
	}
}

// Empty option and value keys are rejected at the schema layer with
// the grammar's own messages.
func TestEmptyScalarsRejected(t *testing.T) {
	ok := "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n"
	for in, msg := range map[string]string{
		ok + "overrides:\n  - files: a\n    option: \"\"\n    value: v\n": "option: empty",
		"plugins:\n  - local: \"\"\n    out: gen\n":                       "empty local value",
	} {
		_, err := Parse([]byte(in))
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: err = %v, want %q", in, err, msg)
		}
	}
}
