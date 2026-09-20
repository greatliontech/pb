package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfetch"
	"github.com/greatliontech/pb/internal/plugoci"
	"github.com/greatliontech/pb/internal/plugrun"
	"github.com/greatliontech/pb/internal/proxy"
	"github.com/greatliontech/pb/internal/trust"
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

// generate selects its runner from the user configuration file
// beneath the environment, and a refusal names the layer the runner
// came from (REQ-plugin-runner-selection, REQ-uc-precedence).
func TestGenerateSelectsFromTheFile(t *testing.T) {
	p := plant(t, "runner: podman\n")
	run := func() error {
		cmd := rootCmd()
		cmd.SetArgs([]string{"generate"})
		return cmd.Execute()
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), `"podman" from the user configuration file `+p+` names no runner`) {
		t.Fatalf("the file's runner: %v", err)
	}
	t.Setenv(userconfig.Keys[userconfig.KeyRunner].Env, "docker")
	if err := run(); err == nil || !strings.Contains(err.Error(), "runner docker (from the PBRUNNER environment variable) is unavailable") {
		t.Fatalf("the environment's runner over the file's: %v", err)
	}
}

// The client's settings are refused naming the layer each came from,
// the default layer included, and the file's relative paths resolve
// against the file's directory (REQ-uc-precedence, REQ-uc-paths).
func TestClientSettingsNameTheirLayer(t *testing.T) {
	p := plant(t, "proxy: \"https://a.example,,direct\"\n")
	var client *modfetch.Client
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

type noDaemonRunner struct{}

func (noDaemonRunner) Run(context.Context, plugrun.Spec) (*plugrun.Result, error) {
	return nil, errors.New("not run")
}
func (noDaemonRunner) Platform() (string, string) { return "linux", "fake" }

type daemonRunner struct{ noDaemonRunner }

func (daemonRunner) RunsDaemonImages() {}

// The acquirer is assembled from the settings and the runner: the
// store beside the module cache, the runner's platform, and the byte
// path the setting selects for that runner (REQ-plugin-core-verifies).
func TestAcquirerConfig(t *testing.T) {
	plant(t, "plugin-pull: docker\n")
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	settings, err := userconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	// The trusted root plugin signatures verify against is the one
	// module evidence verifies against (REQ-prov-plugin-signature).
	s := &dep.Session{Lock: &lockfile.File{}, Client: &modfetch.Client{Policy: &trust.Policy{}, TrustedRoot: sigstoretest.New(t).TrustedRoot()}}
	cfg, err := acquirerConfig(settings, daemonRunner{}, s)
	if err != nil {
		t.Fatal(err)
	}
	want := plugoci.Config{WorkDir: filepath.Join(cacheHome, "pb", "plugins"), EvidenceDir: filepath.Join(cacheHome, "pb", "plugin-evidence"), Lock: s.Lock, Policy: s.Client.Policy, TrustedRoot: s.Client.TrustedRoot, Platform: plugoci.Platform{OS: "linux", Arch: "fake"}, Pull: plugoci.PullDaemon}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
	if _, err := acquirerConfig(settings, noDaemonRunner{}, s); err == nil || !strings.Contains(err.Error(), "the user configuration file ") {
		t.Fatalf("the byte path under a runner without a daemon: %v", err)
	}
}

// The plugin byte path is the store unless the setting says docker,
// which only a runner that runs daemon images accepts; any other
// value names no byte path; each refusal names the layer
// (REQ-plugin-core-verifies, REQ-uc-precedence).
func TestPullMode(t *testing.T) {
	file := "the user configuration file /home/u/.config/pb/config.yaml"
	for _, c := range []struct {
		value  userconfig.Value
		runner plugrun.Runner
		want   plugoci.PullMode
		text   string
	}{
		{userconfig.Value{}, noDaemonRunner{}, plugoci.PullStore, ""},
		{userconfig.Value{Value: "store", From: file}, noDaemonRunner{}, plugoci.PullStore, ""},
		{userconfig.Value{Value: "docker", From: file}, daemonRunner{}, plugoci.PullDaemon, ""},
		{userconfig.Value{Value: "docker", From: file}, noDaemonRunner{}, 0, file + `: plugin-pull "docker": only the docker runner`},
		{userconfig.Value{Value: "docker", From: "the PBPLUGINPULL environment variable"}, noDaemonRunner{}, 0, `the PBPLUGINPULL environment variable: plugin-pull "docker"`},
		{userconfig.Value{Value: "rsync", From: file}, daemonRunner{}, 0, file + `: plugin-pull "rsync" names no byte path (byte paths: store, docker)`},
	} {
		got, err := pullMode(c.value, c.runner)
		if c.text == "" {
			if err != nil || got != c.want {
				t.Errorf("%+v: %v %v", c.value, got, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%+v: %v, want %q", c.value, err, c.text)
		}
	}
}
