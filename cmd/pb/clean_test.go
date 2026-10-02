package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The clean verb runs without a resolution root, empties the module
// cache the settings name, the plugin store's evidence and the
// dependency source store under the user cache directory, and leaves
// the user configuration (REQ-dep-clean).
func TestCleanCommand(t *testing.T) {
	// The cache setting names the configuration's own directory: the
	// configuration and the trusted root beside it are not the cache's.
	config := plant(t, "cache: .\n")
	cache := filepath.Dir(config)
	cacheHome := cacheHome(t)
	origin := strings.Repeat("cd", 32)
	for _, p := range []string{filepath.Join(cache, "root.json"), filepath.Join(cache, "example.com", "m", "@v", "v1.0.0.zip"), filepath.Join(cache, "vcs", origin, "snapshots", "HEAD"), filepath.Join(cache, "vcs", origin, "lock"), filepath.Join(cacheHome, "pb", "plugin-evidence", "sha256", "ab.json"), filepath.Join(cacheHome, "pb", "sources", "example.com", "m@v1.0.0", "m.proto")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"clean"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("pb clean: %v", err)
	}
	if out.String() != "modules: emptied\nplugins: emptied\nsources: emptied\n" {
		t.Fatalf("report = %q", out.String())
	}
	var names []string
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "config.yaml,root.json,vcs" {
		t.Fatalf("the configuration's directory after clean: %v, want the configuration, the trusted root and the vcs store", names)
	}
	if entries, err := os.ReadDir(filepath.Join(cache, "vcs", origin)); err != nil || len(entries) != 1 || entries[0].Name() != "lock" {
		t.Fatalf("the origin after clean: %v, %v; want its lock alone", entries, err)
	}
	if entries, err := os.ReadDir(filepath.Join(cacheHome, "pb", "plugin-evidence")); err != nil || len(entries) != 0 {
		t.Fatalf("evidence after clean: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(cacheHome, "pb", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("an absent plugin store was created by clean: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(cacheHome, "pb", "sources")); err != nil || len(entries) != 0 {
		t.Fatalf("the source store after clean: %v, %v", entries, err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("the user configuration after clean: %v", err)
	}
	cmd, _, err := root.Find([]string{"clean"})
	if err != nil || cmd.Flags().Lookup("modules") == nil || cmd.Flags().Lookup("plugins") == nil || cmd.Flags().Lookup("sources") == nil {
		t.Fatalf("clean's flags: %v", err)
	}
}
