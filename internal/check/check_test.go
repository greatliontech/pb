package check

import "testing"

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
