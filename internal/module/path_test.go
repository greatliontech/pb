package module

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestValidatePath(t *testing.T) {
	good := []string{
		"a.b/x",
		"example.com/m",
		"github.com/greatliontech/pb",
		"pbr.dev/some/deep/sub/tree",
		"a-b.c0/x_y.z~w",
		"buf.build/protocolbuffers/wellknowntypes",
		"example.com/UPPER/Case",
		"x.io/v1",
	}
	for _, p := range good {
		if err := ValidatePath(p); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	bad := []struct{ name, path, msg string }{
		{"empty", "", "needs a hostname"},
		{"no segment", "example.com", "needs a hostname"},
		{"host only with slash", "example.com/", "needs a hostname"},
		{"empty middle segment", "example.com//x", "empty segment"},
		{"no dot in host", "localhost/x", "at least two dot-separated labels"},
		{"host leading dot", ".com/x", "empty label"},
		{"host trailing dot", "example./x", "empty label"},
		{"host empty middle label", "a..b/x", "empty label"},
		{"host label leading hyphen", "-a.b/x", "begins or ends with a hyphen"},
		{"host label trailing hyphen", "a.b-/x", "begins or ends with a hyphen"},
		{"host all hyphens", "-.-/x", "begins or ends with a hyphen"},
		{"host uppercase", "Example.com/x", "hostname contains"},
		{"host underscore", "ex_ample.com/x", "hostname contains"},
		{"host brace", "ex{a.mp/x", "hostname contains"},
		{"scheme", "https://example.com/x", "at least two dot-separated labels"},
		{"port", "example.com:8080/x", "hostname contains"},
		{"query", "example.com/x?y", "segment contains"},
		{"fragment", "example.com/x#y", "segment contains"},
		{"segment leading dot", "example.com/.hidden", "begins or ends with a dot"},
		{"segment trailing dot", "example.com/x.", "begins or ends with a dot"},
		{"segment colon", "example.com/x:y", "segment contains"},
		{"segment space", "example.com/x y", "segment contains"},
		{"segment unicode", "example.com/héllo", "segment contains"},
		{"segment brace", "example.com/x{y", "segment contains"},
		{"segment bracket", "example.com/x[y", "segment contains"},
		{"segment backslash", `example.com/x\y`, "segment contains"},
		{"host dangling dot", "a./x", "empty label"},
	}
	for _, tc := range bad {
		err := ValidatePath(tc.path)
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s (%q): err = %v, want ErrInvalidPath", tc.name, tc.path, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s (%q): err %q does not name %q", tc.name, tc.path, err, tc.msg)
		}
	}
}

// Property: every path assembled from the valid alphabets passes, and
// corrupting any single position with a forbidden character fails.
func TestValidatePathProperty(t *testing.T) {
	hostEdge := []rune("abcdefghijklmnopqrstuvwxyz0123456789")
	hostAlpha := []rune("abcdefghijklmnopqrstuvwxyz0123456789-")
	segAlpha := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_~")
	rapid.Check(t, func(t *rapid.T) {
		// Labels are valid by construction: hyphen-free first and last char.
		label := func(name string) string {
			mid := string(rapid.SliceOfN(rapid.SampledFrom(hostAlpha), 0, 4).Draw(t, name+"Mid"))
			edge := string(rapid.SampledFrom(hostEdge).Draw(t, name+"Edge"))
			if mid == "" {
				return edge
			}
			return edge + mid[:max(0, len(mid)-1)] + edge
		}
		host := label("hostA") + "." + label("hostB")
		segs := rapid.IntRange(1, 4).Draw(t, "segs")
		parts := []string{host}
		for range segs {
			parts = append(parts, string(rapid.SliceOfN(rapid.SampledFrom(segAlpha), 1, 8).Draw(t, "seg")))
		}
		path := strings.Join(parts, "/")
		if err := ValidatePath(path); err != nil {
			t.Fatalf("constructed path %q rejected: %v", path, err)
		}
		// Corrupt one byte with a forbidden character.
		forbidden := rapid.SampledFrom([]rune{':', '?', '#', ' ', '"', '\'', '\\', '\n'}).Draw(t, "forbidden")
		pos := rapid.IntRange(0, len(path)-1).Draw(t, "pos")
		corrupted := path[:pos] + string(forbidden) + path[pos+1:]
		if err := ValidatePath(corrupted); err == nil {
			t.Fatalf("corrupted path %q accepted", corrupted)
		}
	})
}

func FuzzValidatePath(f *testing.F) {
	f.Add("example.com/m")
	f.Add("")
	f.Add("https://x.y/z")
	f.Fuzz(func(t *testing.T, p string) {
		// Never panics; acceptance implies the structural facts the rest of
		// the system relies on.
		if err := ValidatePath(p); err == nil {
			if strings.ContainsAny(p, ":?# \"'\\\n\t") {
				t.Fatalf("accepted path %q with unsafe character", p)
			}
			host, rest, ok := strings.Cut(p, "/")
			if !ok || host == "" || rest == "" || !strings.Contains(host, ".") {
				t.Fatalf("accepted path %q without host/segment shape", p)
			}
		}
	})
}
