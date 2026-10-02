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
	"github.com/greatliontech/pb/internal/plugin"
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
func (noDaemonRunner) Platform() plugin.Platform { return plugin.Platform{OS: "linux", Arch: "fake"} }

type daemonRunner struct{ noDaemonRunner }

func (daemonRunner) RunsDaemonImages() {}

// The acquirer is assembled from the session: the store beside the
// module cache, the lockfile and the trust policy; the substrates it
// serves are the selection's, under the floor, the daemon byte path
// on the docker runner's alone where selected
// (REQ-plugin-core-verifies, REQ-plugin-runner-selection).
func TestAcquirerConfig(t *testing.T) {
	cacheHome := cacheHome(t)
	// The trusted root plugin signatures verify against is the one
	// module evidence verifies against (REQ-prov-plugin-signature).
	s := &dep.Session{Lock: &lockfile.File{}, Client: &fetch.Client{Policy: &trust.Policy{}, TrustedRoot: sigstoretest.New(t).TrustedRoot()}}
	cfg, err := acquirerConfig(s)
	if err != nil {
		t.Fatal(err)
	}
	want := oci.Config{WorkDir: filepath.Join(cacheHome, "pb", "plugins"), EvidenceDir: filepath.Join(cacheHome, "pb", "plugin-evidence"), Lock: s.Lock, Policy: s.Client.Policy, TrustedRoot: s.Client.TrustedRoot}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
	docker, _ := runner.Only(runner.RunnerDocker, daemonRunner{}).Candidates(context.Background(), plugin.TierStrong)
	if got, _, withheld := dep.Substrates(docker, true, true); withheld != "" || !reflect.DeepEqual(got, []oci.Candidate{{Platform: plugin.Platform{OS: "linux", Arch: "fake"}, Daemon: true}}) {
		t.Fatalf("the docker runner's substrate under the daemon byte path: %+v (%q)", got, withheld)
	}
	native, _ := runner.Only(runner.RunnerNative, noDaemonRunner{}).Candidates(context.Background(), plugin.TierStrong)
	if got, _, withheld := dep.Substrates(native, false, true); withheld != "" || !reflect.DeepEqual(got, []oci.Candidate{{Platform: plugin.Platform{OS: "linux", Arch: "fake"}}}) {
		t.Fatalf("the native runner's substrate: %+v (%q)", got, withheld)
	}
}

// The plugin byte path is the store unless the setting says docker,
// which only a runner that runs daemon images accepts; any other
// value names no byte path; each refusal names the layer
// (REQ-plugin-core-verifies, REQ-uc-precedence).
func TestPullMode(t *testing.T) {
	file := "the user configuration file /home/u/.config/pb/config.yaml"
	native, docker := runner.Only(runner.RunnerNative, noDaemonRunner{}), runner.Only(runner.RunnerDocker, daemonRunner{})
	for _, c := range []struct {
		value userconfig.Value
		sel   *runner.Selection
		want  bool
		text  string
	}{
		{userconfig.Value{}, native, false, ""},
		{userconfig.Value{Value: "store", From: file}, native, false, ""},
		{userconfig.Value{Value: "docker", From: file}, docker, true, ""},
		{userconfig.Value{Value: "docker", From: file}, native, false, file + `: plugin-pull "docker": only the docker runner has a daemon to pull plugins, and the caller names runner native for every entry, which runs no daemon images`},
		{userconfig.Value{Value: "docker", From: "the PBPLUGINPULL environment variable"}, native, false, `the PBPLUGINPULL environment variable: plugin-pull "docker"`},
		{userconfig.Value{Value: "rsync", From: file}, docker, false, file + `: plugin-pull "rsync" names no byte path (byte paths: store, docker)`},
	} {
		got, err := pullMode(c.value, c.sel)
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

// An update offers every runner the host has as a substrate, the
// docker runner's included where the platform's export cannot be
// streamed: an update streams nothing (REQ-dep-update,
// REQ-plugin-platform-strict).
func TestUpdateSubstratesOfferEveryRunner(t *testing.T) {
	cands := []runner.Candidate{{Name: runner.RunnerNative, Runner: noDaemonRunner{}}, {Name: runner.RunnerDocker, Runner: daemonRunner{}}}
	got := updateSubstrates(cands)
	want := []oci.Candidate{{Platform: plugin.Platform{OS: "linux", Arch: "fake"}}, {Platform: plugin.Platform{OS: "linux", Arch: "fake"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("an update's substrates = %+v, want %+v", got, want)
	}
}
