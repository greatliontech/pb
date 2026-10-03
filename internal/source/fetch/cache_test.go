package fetch

import (
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/module/archive"
	"pgregory.net/rapid"
)

// The cache's own names under an @v directory: an artifact of one of
// the kinds, or an atomic write's temporary; nothing else is the
// cache's (REQ-dep-cache-layout).
func TestIsArtifactName(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	for name, want := range map[string]bool{
		"v1.0.0.pb1-" + hex + ".zip": true, "v1.0.0.pb1-" + hex + ".mod": true, "v1.0.0.pb1-" + hex + ".prov": true, "v1.0.0.info": true, ".put-123": true,
		// The layout before the digest: read by nothing, the emptying's.
		"v1.0.0.zip": true, "v1.0.0.mod": true, "v1.0.0.prov": true,
		".zip": false, "notes.txt": false, "v1.0.0.tar": false, "zip": false, "README": false,
		// A stem with no well-formed digest name at its end is the
		// earlier layout's, whatever its version spelled.
		"v1.0.0.pb1-abc.zip": true, "v1.0.0.pb1-" + strings.ToUpper(hex) + ".zip": true, "v1.0.0-rc.pb1-abc.zip": true, ".pb1-" + hex + ".zip": true,
	} {
		if got := IsArtifactName(name); got != want {
			t.Errorf("IsArtifactName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A cache-relative directory is a module's exactly when it unescapes
// to a module path (REQ-dep-cache-layout).
func TestIsModuleDir(t *testing.T) {
	for escaped, want := range map[string]bool{
		"example.com/m": true, "github.com/!org/!repo": true,
		"cache/download/example.com/m": false, "example.com": false, "m": false, "example.com/!": false,
	} {
		if got := IsModuleDir(escaped); got != want {
			t.Errorf("IsModuleDir(%q) = %v, want %v", escaped, got, want)
		}
	}
}

// The digest's file-name spelling and its reading are one another's
// inverse for every module digest and every base: what DigestName
// writes under a base, SplitDigest reads back as that base and that
// digest, and a tail that is no module digest's spelling reads as no
// digest (REQ-dep-cache-layout).
func TestDigestNameRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		digest := archive.DigestPrefix + rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "hex")
		base := rapid.StringMatching(`[A-Za-z0-9!._-]{1,20}`).Draw(t, "base")
		got, d, ok := SplitDigest(base + "." + DigestName(digest))
		if !ok || got != base || d != digest {
			t.Fatalf("SplitDigest(%s.%s) = %q, %q, %v", base, DigestName(digest), got, d, ok)
		}
	})
	for _, name := range []string{"v1.0.0", "v1.0.0.pb1-abc", "v1.0.0.pb2-" + strings.Repeat("ab", 32), "v1.0.0.pb1-" + strings.ToUpper(strings.Repeat("ab", 32)), ".pb1-" + strings.Repeat("ab", 32), "v1.0.0.pb1:" + strings.Repeat("ab", 32)} {
		if base, d, ok := SplitDigest(name); ok || base != name || d != "" {
			t.Errorf("SplitDigest(%q) = %q, %q, %v, want the name whole and no digest", name, base, d, ok)
		}
	}
}
