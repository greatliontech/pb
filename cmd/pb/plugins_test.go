package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin/oci"
	"github.com/greatliontech/pb/internal/plugin/runner"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/userconfig"
)

type noDaemonRunner struct{}

func (noDaemonRunner) Run(context.Context, runner.Spec) (*runner.Result, error) {
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
	s := &dep.Session{Lock: &lockfile.File{}, Client: &fetch.Client{Policy: &trust.Policy{}, TrustedRoot: sigstoretest.New(t).TrustedRoot()}}
	cfg, err := acquirerConfig(settings, daemonRunner{}, s)
	if err != nil {
		t.Fatal(err)
	}
	want := oci.Config{WorkDir: filepath.Join(cacheHome, "pb", "plugins"), EvidenceDir: filepath.Join(cacheHome, "pb", "plugin-evidence"), Lock: s.Lock, Policy: s.Client.Policy, TrustedRoot: s.Client.TrustedRoot, Platform: oci.Platform{OS: "linux", Arch: "fake"}, Pull: oci.PullDaemon}
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
		value userconfig.Value
		run   runner.Runner
		want  oci.PullMode
		text  string
	}{
		{userconfig.Value{}, noDaemonRunner{}, oci.PullStore, ""},
		{userconfig.Value{Value: "store", From: file}, noDaemonRunner{}, oci.PullStore, ""},
		{userconfig.Value{Value: "docker", From: file}, daemonRunner{}, oci.PullDaemon, ""},
		{userconfig.Value{Value: "docker", From: file}, noDaemonRunner{}, 0, file + `: plugin-pull "docker": only the docker runner`},
		{userconfig.Value{Value: "docker", From: "the PBPLUGINPULL environment variable"}, noDaemonRunner{}, 0, `the PBPLUGINPULL environment variable: plugin-pull "docker"`},
		{userconfig.Value{Value: "rsync", From: file}, daemonRunner{}, 0, file + `: plugin-pull "rsync" names no byte path (byte paths: store, docker)`},
	} {
		got, err := pullMode(c.value, c.run)
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

// The update verb's plugin updater opens the acquirer only on the
// first plugin named: a module update needs no runner, and a runner
// the settings select but the host lacks fails the plugin update
// alone (REQ-dep-update).
func TestLazyUpdaterOpensOnFirstPlugin(t *testing.T) {
	plant(t, "runner: docker\n")
	t.Setenv("DOCKER_HOST", "unix:///nonexistent.sock")
	settings, err := userconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	s := &dep.Session{Lock: &lockfile.File{}, Client: &fetch.Client{Policy: &trust.Policy{}}}
	up := &lazyUpdater{settings: settings, session: s}
	up.Close()
	if up.acq != nil {
		t.Fatal("an updater opened an acquirer before any plugin was named")
	}
	if _, _, err := up.UpdatePlugin(context.Background(), "ghcr.io/o/p:v1"); err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("a plugin named under an unavailable runner: %v", err)
	}
	up.Close()
}
