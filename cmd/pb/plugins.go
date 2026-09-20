package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugoci"
	"github.com/greatliontech/pb/internal/plugrun"
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
func acquirerConfig(settings *userconfig.Settings, runner plugrun.Runner, s *dep.Session) (plugoci.Config, error) {
	pull, err := pullMode(settings.Get(userconfig.KeyPluginPull), runner)
	if err != nil {
		return plugoci.Config{}, err
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return plugoci.Config{}, fmt.Errorf("resolving the user cache directory for the plugin store (set XDG_CACHE_HOME or HOME): %w", err)
	}
	os_, arch := runner.Platform()
	return plugoci.Config{
		WorkDir:     filepath.Join(base, "pb", "plugins"),
		EvidenceDir: filepath.Join(base, "pb", "plugin-evidence"),
		Lock:        s.Lock,
		Policy:      s.Client.Policy,
		TrustedRoot: s.Client.TrustedRoot,
		Platform:    plugoci.Platform{OS: os_, Arch: arch},
		Pull:        pull,
	}, nil
}

// pullMode reads the plugin-pull setting: the store by default, the
// daemon where stated and the runner runs daemon images; a value
// naming neither byte path, or the daemon under a runner that runs
// none, is refused naming the layer the value came from.
func pullMode(v userconfig.Value, runner plugrun.Runner) (plugoci.PullMode, error) {
	switch v.Value {
	case "", pullStore:
		return plugoci.PullStore, nil
	case pullDaemon:
		if _, daemon := runner.(plugrun.DaemonImages); !daemon {
			return 0, v.Wrap(fmt.Errorf("plugin-pull %q: only the docker runner has a daemon to pull plugins (select it with --%s docker)", v.Value, plugrun.FlagRunner))
		}
		return plugoci.PullDaemon, nil
	}
	return 0, v.Wrap(fmt.Errorf("plugin-pull %q names no byte path (byte paths: %s, %s)", v.Value, pullStore, pullDaemon))
}

// lazyUpdater is the update verb's plugin updater, the acquirer
// opened on the first plugin named and closed with the verb.
type lazyUpdater struct {
	settings *userconfig.Settings
	session  *dep.Session
	acq      *plugoci.Acquirer
}

func (u *lazyUpdater) UpdatePlugin(ctx context.Context, ref string) (lockfile.PluginPin, lockfile.PluginPin, error) {
	if u.acq == nil {
		runner, err := plugrun.Open(nil, u.settings.Get(userconfig.KeyRunner))
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		cfg, err := acquirerConfig(u.settings, runner, u.session)
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		acq, err := plugoci.New(cfg)
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
