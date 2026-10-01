package main

import (
	"path/filepath"
	"testing"

	"github.com/greatliontech/pb/internal/userconfig"
)

// The default module cache lives under the platform's user cache
// directory, which the platform's own variable relocates
// (platforms.md REQ-plat-user-dirs): pb/mod beneath it on every
// platform, a stated cache setting winning over it.
func TestModuleCacheDirDefault(t *testing.T) {
	base := t.TempDir()
	loc := userconfig.CacheLocation()
	t.Setenv(loc.Var, base)
	t.Setenv("PBCACHE", "")
	settings, err := userconfig.LoadFile(filepath.Join(t.TempDir(), "absent.yaml"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	got, err := moduleCacheDir(settings)
	if err != nil || got.Value != filepath.Join(loc.Dir(base), "pb", "mod") {
		t.Fatalf("the default cache = %+v, %v; want under %s", got, err, loc.Dir(base))
	}
	stated, err := userconfig.LoadFile(filepath.Join(t.TempDir(), "absent.yaml"), func(k string) string {
		if k == "PBCACHE" {
			return filepath.Join(base, "elsewhere")
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := moduleCacheDir(stated); err != nil || got.Value != filepath.Join(base, "elsewhere") {
		t.Fatalf("a stated cache = %+v, %v", got, err)
	}
}
