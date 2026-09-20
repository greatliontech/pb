package check

import (
	"strings"
	"testing"
)

// The vocabulary parses exactly its spellings, the lists are copies,
// and environment 1 is the one provided.
func TestVocabulary(t *testing.T) {
	for _, target := range Targets() {
		if got, ok := ParseTarget(string(target)); !ok || got != target {
			t.Errorf("target %q refused", target)
		}
	}
	for _, s := range []string{"", "File", "fields", "oneofs", "lint", "error"} {
		if _, ok := ParseTarget(s); ok {
			t.Errorf("target %q accepted", s)
		}
	}
	if k, ok := ParseKind("lint"); !ok || k != KindLint {
		t.Error("lint")
	}
	if k, ok := ParseKind("breaking"); !ok || k != KindBreaking {
		t.Error("breaking")
	}
	for _, s := range []string{"Lint", "", "style"} {
		if _, ok := ParseKind(s); ok {
			t.Errorf("kind %q accepted", s)
		}
	}
	if sv, ok := ParseSeverity("error"); !ok || sv != SeverityError {
		t.Error("error")
	}
	if sv, ok := ParseSeverity("warning"); !ok || sv != SeverityWarning {
		t.Error("warning")
	}
	for _, s := range []string{"info", "", "Error"} {
		if _, ok := ParseSeverity(s); ok {
			t.Errorf("severity %q accepted", s)
		}
	}
	if !ProvidesEnvironment(1) || ProvidesEnvironment(0) || ProvidesEnvironment(2) {
		t.Error("environments")
	}
	if e := Environments(); len(e) != 1 || e[0] != 1 {
		t.Errorf("environments = %v", e)
	}
	// The lists are the caller's to change, not the vocabulary's.
	Targets()[0] = "x"
	Environments()[0] = 9
	if Targets()[0] != TargetFile || Environments()[0] != 1 {
		t.Error("a caller rewrote the vocabulary")
	}
}

// Findings print as one line each and sort by path, then line and
// column, then rule id, then message, the location-less last
// (REQ-check-findings-output); errors fail the run, warnings never
// (REQ-check-exit-status).
func TestFindingsOutput(t *testing.T) {
	fs := []Finding{
		{RuleID: "SET", Severity: SeverityWarning, Message: "s"},
		{RuleID: "B", Severity: SeverityError, Message: "m", Path: "b.proto", Line: 2, Column: 1},
		{RuleID: "PKG", Severity: SeverityWarning, Message: "p", Path: "a.proto"},
		{RuleID: "A", Severity: SeverityError, Message: "z", Path: "a.proto", Line: 3, Column: 5, Base: true},
		{RuleID: "A", Severity: SeverityError, Message: "m", Path: "a.proto", Line: 3, Column: 5},
		{RuleID: "C", Severity: SeverityWarning, Message: "c", Path: "a.proto", Line: 3, Column: 2},
		{RuleID: "ALL", Severity: SeverityError, Message: "a"},
	}
	Sort(fs)
	var got []string
	for _, f := range fs {
		got = append(got, f.String())
	}
	want := []string{
		"a.proto: warning PKG: p",
		"a.proto:3:2: warning C: c",
		"a.proto:3:5: error A: m",
		"a.proto:3:5: error A: z [base]",
		"b.proto:2:1: error B: m",
		"error ALL: a",
		"warning SET: s",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !Failing(fs) || Failing([]Finding{{Severity: SeverityWarning}}) || Failing(nil) {
		t.Error("failing")
	}
}
