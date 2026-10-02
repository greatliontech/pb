package modfiles

import "testing"

// WellKnownSet is the toolchain's embedded set whole: every entry a
// well-known import with its bytes, the descriptor and the compiler
// plugin's file among them, nothing a module provides (lsp.md
// REQ-lsp-dependency-files).
func TestWellKnownSet(t *testing.T) {
	set, err := WellKnownSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"google/protobuf/any.proto", "google/protobuf/descriptor.proto", "google/protobuf/compiler/plugin.proto"} {
		if len(set[p]) == 0 {
			t.Fatalf("%s is not in the set: %v", p, len(set))
		}
	}
	for p, b := range set {
		if !WellKnown(p) {
			t.Fatalf("%s is in the set but no well-known import", p)
		}
		if len(b) == 0 {
			t.Fatalf("%s has no bytes", p)
		}
	}
	if _, ok := set["google/protobuf/go_features.proto"]; ok {
		t.Fatal("go_features.proto is in the set, which the toolchain does not ship")
	}
}
