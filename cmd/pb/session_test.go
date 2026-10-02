package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the user configuration file "+p+": gitprov: read trusted root "+strconv.Quote(filepath.Join(filepath.Dir(p), "root.json"))) {
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
	// A user cache directory that cannot be created: a regular file
	// stands where the platform's directory would be, under the
	// variable's value — the home's on darwin, where the
	// configuration moves with it and an absent file is an empty
	// configuration, which "nothing stated" is.
	cache := userconfig.CacheLocation()
	blocked := filepath.Join(t.TempDir(), "cache")
	if cache.Sub == "" {
		if err := os.WriteFile(blocked, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(cache.Dir(blocked)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache.Dir(blocked), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(cache.Var, blocked)
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the platform default: module cache: ") {
		t.Fatalf("the default cache: %v", err)
	}
	plant(t, "noproxy: corp.example.com\n")
	cacheHome := cacheHome(t)
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

// The private-origin settings: a credential file a layer states must
// exist and parse, refused naming the layer (module-resolution.md, the
// credential file term); the default location absent is empty; the
// ssh setting's patterns are refused naming the layer, and a module
// they match resolves to an SSH origin through the assembled client
// (REQ-resolve-ssh).
func TestClientPrivateOriginSettings(t *testing.T) {
	var client *fetch.Client
	assemble := func() error {
		settings, err := userconfig.Load()
		if err != nil {
			return err
		}
		client, err = assembleClient(settings)
		return err
	}
	p := plant(t, "netrc: creds\n")
	cacheHome(t) // the default cache under the test, never the developer's
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the user configuration file "+p+": netrc: open "+filepath.Join(filepath.Dir(p), "creds")) {
		t.Fatalf("the file's absent credential file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "netrc")
	os.WriteFile(bad, []byte("machine a.example port 1\n"), 0o600)
	t.Setenv("PBNETRC", bad)
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the PBNETRC environment variable: invalid credential file: "+bad) {
		t.Fatalf("the environment's malformed credential file: %v", err)
	}
	t.Setenv("PBNETRC", "")

	plant(t, "# nothing stated\n")
	// The cache stated, so the home decides the credential file's
	// default location alone (darwin derives the cache from it too).
	t.Setenv("PBCACHE", t.TempDir())
	t.Setenv(homeVar(), t.TempDir()) // no credential file at the default location
	if err := assemble(); err != nil {
		t.Fatalf("the default location absent: %v", err)
	}
	t.Setenv(homeVar(), "") // no home to derive the default location from
	if err := assemble(); err != nil {
		t.Fatalf("no home: %v", err)
	}
	t.Setenv(homeVar(), t.TempDir())
	t.Setenv("PBCACHE", "")
	t.Setenv("PBSSH", "corp.example.com,[")
	if err := assemble(); err == nil || !strings.Contains(err.Error(), "the PBSSH environment variable: invalid ssh setting: ssh pattern \"[\": syntax error in pattern") {
		t.Fatalf("the environment's ssh patterns: %v", err)
	}
	t.Setenv("PBSSH", "corp.example.com")
	if err := assemble(); err != nil {
		t.Fatal(err)
	}
	o, err := client.ResolveOrigin(context.Background(), "corp.example.com/r.git/sub")
	if err != nil || o.Repo != "ssh://git@corp.example.com/r.git" || o.Subtree != "sub" {
		t.Fatalf("an ssh-routed module's origin: %+v, %v", o, err)
	}
	o, err = client.ResolveOrigin(context.Background(), "other.example.com/r.git")
	if err != nil || o.Repo != "https://other.example.com/r.git" {
		t.Fatalf("an unmatched module's origin: %+v, %v", o, err)
	}
}
