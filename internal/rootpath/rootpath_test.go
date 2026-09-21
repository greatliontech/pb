package rootpath

import (
	"path"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Check's written-spelling table: clean contained paths pass, the root
// itself passes, and each rejection class names its cause.
func TestCheck(t *testing.T) {
	for _, s := range []string{".", "a", "a/b", "..a", "a/..b", "a.."} {
		if err := Check(s, "the root"); err != nil {
			t.Errorf("Check(%q) = %v", s, err)
		}
	}
	rejects := map[string]string{
		"":        "empty path",
		"/":       "is absolute; paths are relative to the root",
		"/a":      "is absolute",
		"..":      "escapes the root",
		"../a":    "escapes the root",
		"a/../..": "is not a clean path",
		"a/":      "is not a clean path",
		"./a":     "is not a clean path",
		"a//b":    "is not a clean path",
		"a/./b":   "is not a clean path",
	}
	for s, want := range rejects {
		err := Check(s, "the root")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check(%q) = %v, want %q", s, err, want)
		}
	}
}

// Clean normalizes before judging: an unclean spelling that stays
// inside passes cleaned, one that escapes once cleaned is refused.
func TestClean(t *testing.T) {
	accepts := map[string]string{
		"a/":        "a",
		"./a/./b":   "a/b",
		"a/../b":    "b",
		"a/b/..":    "a",
		".":         ".",
		"./":        ".",
		"a/..":      ".",
		"a//b":      "a/b",
		"..a/b":     "..a/b",
		"x/../../y": "",
	}
	for s, want := range accepts {
		got, err := Clean(s, "the root")
		if want == "" {
			if err == nil || !strings.Contains(err.Error(), "escapes the root") {
				t.Errorf("Clean(%q) = %q, %v; want escape", s, got, err)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", s, got, err, want)
		}
	}
	for s, want := range map[string]string{"": "empty path", "/a": "is absolute", "..": "escapes the root", "../a": "escapes the root"} {
		if _, err := Clean(s, "the root"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Clean(%q) = %v, want %q", s, err, want)
		}
	}
}

// Property: Check accepts exactly the values Clean accepts unchanged,
// and every accepted value, joined under a root, stays inside it.
func TestContainmentProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		seg := rapid.SampledFrom([]string{"a", "b", "..", ".", "", "c.go", "..d", "e.."})
		n := rapid.IntRange(1, 5).Draw(rt, "n")
		parts := make([]string, n)
		for i := range parts {
			parts[i] = seg.Draw(rt, "seg")
		}
		s := strings.Join(parts, "/")
		if rapid.Bool().Draw(rt, "abs") {
			s = "/" + s
		}
		cleaned, cleanErr := Clean(s, "r")
		checkErr := Check(s, "r")
		if (checkErr == nil) != (cleanErr == nil && cleaned == s) {
			rt.Fatalf("Check(%q) = %v but Clean = %q, %v", s, checkErr, cleaned, cleanErr)
		}
		if cleanErr == nil {
			joined := path.Join("/root", cleaned)
			if joined != "/root" && !strings.HasPrefix(joined, "/root/") {
				rt.Fatalf("Clean(%q) = %q resolves to %q, outside the root", s, cleaned, joined)
			}
		} else if s != "" && !strings.HasPrefix(s, "/") {
			// The only remaining rejection is an escape: the cleaned
			// form must actually leave the root.
			if joined := path.Join("/root", path.Clean(s)); joined == "/root" || strings.HasPrefix(joined, "/root/") {
				rt.Fatalf("Clean(%q) refused a contained path: %v", s, cleanErr)
			}
		}
	})
}

// Contains: the root holds every other directory, a directory those
// beneath it, none itself, and a sibling with a shared prefix none.
func TestContains(t *testing.T) {
	for _, c := range []struct {
		outer, inner string
		want         bool
	}{
		{".", "a", true}, {".", "a/b", true}, {".", ".", false},
		{"a", "a/b", true}, {"a", "a/b/c", true}, {"a", "a", false},
		{"a", "ab", false}, {"a/b", "a", false}, {"a", ".", false},
	} {
		if got := Contains(c.outer, c.inner); got != c.want {
			t.Errorf("Contains(%q, %q) = %v", c.outer, c.inner, got)
		}
	}
}
