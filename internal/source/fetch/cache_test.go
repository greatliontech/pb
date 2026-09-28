package fetch

import "testing"

// The cache's own names under an @v directory: an artifact of one of
// the kinds, or an atomic write's temporary; nothing else is the
// cache's (REQ-dep-cache-layout).
func TestIsArtifactName(t *testing.T) {
	for name, want := range map[string]bool{
		"v1.0.0.zip": true, "v1.0.0.mod": true, "v1.0.0.info": true, "v1.0.0.prov": true, ".put-123": true,
		".zip": false, "notes.txt": false, "v1.0.0.tar": false, "zip": false, "README": false,
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
