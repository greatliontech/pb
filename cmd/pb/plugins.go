package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin/oci"
	"github.com/greatliontech/pb/internal/plugin/runner"
	"github.com/greatliontech/pb/internal/userconfig"
)

// The plugin byte path's spellings (plugin-execution.md
// REQ-plugin-core-verifies).
const (
	pullStore  = "store"
	pullDaemon = "docker"
)

// acquirerConfig assembles the plugin acquirer: pb's plugin store and
// the evidence kept with it, which sit beside the module cache rather
// than inside it (the module cache root holds module artifacts only,
// dep-verbs.md REQ-dep-cache-layout), the session's lockfile and trust
// policy, the runner's platform, and the byte path the settings
// select for that runner.
func acquirerConfig(settings *userconfig.Settings, run runner.Runner, s *dep.Session) (oci.Config, error) {
	pull, err := pullMode(settings.Get(userconfig.KeyPluginPull), run)
	if err != nil {
		return oci.Config{}, err
	}
	workDir, evidenceDir, err := pluginStoreDirs()
	if err != nil {
		return oci.Config{}, err
	}
	return oci.Config{
		WorkDir:     workDir,
		EvidenceDir: evidenceDir,
		Lock:        s.Lock,
		Policy:      s.Client.Policy,
		TrustedRoot: s.Client.TrustedRoot,
		Platform:    run.Platform(),
		Pull:        pull,
	}, nil
}

// pullMode reads the plugin-pull setting: the store by default, the
// daemon where stated and the runner runs daemon images; a value
// naming neither byte path, or the daemon under a runner that runs
// none, is refused naming the layer the value came from.
func pullMode(v userconfig.Value, run runner.Runner) (oci.PullMode, error) {
	switch v.Value {
	case "", pullStore:
		return oci.PullStore, nil
	case pullDaemon:
		if _, daemon := run.(runner.DaemonImages); !daemon {
			return 0, v.Wrap(fmt.Errorf("plugin-pull %q: only the docker runner has a daemon to pull plugins (select it with --%s docker)", v.Value, runner.FlagRunner))
		}
		return oci.PullDaemon, nil
	}
	return 0, v.Wrap(fmt.Errorf("plugin-pull %q names no byte path (byte paths: %s, %s)", v.Value, pullStore, pullDaemon))
}

// lazyUpdater is the update verb's plugin updater, the acquirer
// opened on the first plugin named and closed with the verb.
type lazyUpdater struct {
	settings *userconfig.Settings
	session  *dep.Session
	acq      *oci.Acquirer
}

func (u *lazyUpdater) UpdatePlugin(ctx context.Context, ref string) (lockfile.PluginPin, lockfile.PluginPin, error) {
	if u.acq == nil {
		run, err := runner.Open(nil, u.settings.Get(userconfig.KeyRunner))
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		cfg, err := acquirerConfig(u.settings, run, u.session)
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		acq, err := oci.New(cfg)
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		u.acq = acq
	}
	return u.acq.UpdatePlugin(ctx, ref)
}

// Close releases the acquirer if one was opened; a second close is
// nothing.
func (u *lazyUpdater) Close() error {
	if u.acq == nil {
		return nil
	}
	acq := u.acq
	u.acq = nil
	return acq.Close()
}

// pluginStoreDirs locates the plugin store and its kept evidence
// under the platform user cache directory, beside the module cache's
// default rather than inside it (plugin-execution.md
// REQ-plugin-core-verifies, provenance.md
// REQ-prov-plugin-evidence-store).
func pluginStoreDirs() (workDir, evidenceDir string, err error) {
	base, err := userconfig.UserCacheDir()
	if err != nil {
		return "", "", fmt.Errorf("resolving the user cache directory for the plugin store (set XDG_CACHE_HOME or HOME): %w", err)
	}
	return filepath.Join(base, "pb", "plugins"), filepath.Join(base, "pb", "plugin-evidence"), nil
}
