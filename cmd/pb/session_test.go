package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/userconfig"
)

// The client's settings are refused naming the layer each came from,
// the default layer included, and the file's relative paths resolve
// against the file's directory (REQ-uc-precedence, REQ-uc-paths).
func TestClientSettingsNameTheirLayer(t *testing.T) {
	p := plant(t, "proxy: \"https://a.example,,direct\"\n")
	var client *fetch.Client
	assemble := func() error {
		settings, err := userconfig.Load()
		if err != nil {
			return err
		}
		client, err = assembleClient(settings)
		return err
	}
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the user configuration file "+p+": invalid proxy configuration: the source list carries an empty entry") {
		t.Fatalf("the file's proxy: %v", err)
	}

	t.Setenv("PBPROXY", "direct") // the environment's proxy over the file's
	t.Setenv("PBNOPROXY", "corp.example.com,")
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the PBNOPROXY environment variable: invalid proxy configuration: the exclusion list carries an empty pattern") {
		t.Fatalf("the environment's noproxy: %v", err)
	}
	t.Setenv("PBPROXY", "")
	t.Setenv("PBNOPROXY", "")

	p = plant(t, "cache: mod\ntrustedroot: root.json\n")
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the user configuration file "+p+": gitprov: read trusted root "+`"`+filepath.Join(filepath.Dir(p), "root.json")+`"`) {
		t.Fatalf("the file's trusted root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), "mod")); err != nil {
		t.Fatalf("the file's relative cache was not created beside it: %v", err)
	}

	t.Setenv("PBCACHE", filepath.Join(os.DevNull, "mod"))
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the PBCACHE environment variable: module cache: ") {
		t.Fatalf("the environment's cache: %v", err)
	}
	t.Setenv("PBCACHE", "")

	// The default cache is pb/mod under the platform user cache
	// directory (dep-verbs.md, the module cache term), refused under
	// the default layer's name.
	plant(t, "# nothing stated\n")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.DevNull, "cache"))
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the platform default: module cache: ") {
		t.Fatalf("the default cache: %v", err)
	}
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	plant(t, "noproxy: corp.example.com\n")
	t.Setenv("PBPROXY", "https://p.example")
	if err := assemble(); err != nil {
		t.Fatal(err)
	}
	if root := client.Cache.FS.(interface{ Root() string }).Root(); root != filepath.Join(cacheHome, "pb", "mod") {
		t.Fatalf("the default cache is at %q", root)
	}
	if _, err := os.Stat(filepath.Join(cacheHome, "pb", "mod")); err != nil {
		t.Fatalf("the default cache was not created: %v", err)
	}
	// A successful client routes by both settings as parsed: the
	// environment's sources and the file's exclusions.
	want := proxy.Config{Sources: []proxy.Source{{URL: "https://p.example"}}, NoProxy: []string{"corp.example.com"}}
	if !reflect.DeepEqual(client.Sources, want) {
		t.Fatalf("Sources = %+v, want %+v", client.Sources, want)
	}
}
