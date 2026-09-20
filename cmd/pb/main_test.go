package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/greatliontech/pb/internal/userconfig"
)

// plant writes a user configuration file under a fresh configuration
// directory and returns the file's path.
func plant(t *testing.T, content string) string {
	t.Helper()
	if userconfig.LocationVar() == "" {
		t.Skip("the user configuration directory is not relocatable by environment on this platform")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "pb", userconfig.FileName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range userconfig.Keys {
		t.Setenv(s.Env, "")
	}
	t.Setenv("PATH", t.TempDir()) // no docker here
	return p
}
