package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/greatliontech/pb/internal/plugin"

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
// dep-verbs.md REQ-dep-cache-layout), and the session's lockfile and
// trust policy.
func acquirerConfig(s *dep.Session) (oci.Config, error) {
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
	}, nil
}

// updateSubstrates are the substrates an update offers: every runner
// the host has, whatever its row and whether or not its export
// streams — an update runs nothing and streams nothing, the pin it
// moves being the lockfile's for every machine (dep-verbs.md
// REQ-dep-update); the floor and the byte path govern a run, at
// generate.
func updateSubstrates(cands []runner.Candidate) []oci.Candidate {
	substrates := dep.Substrates(cands, false)
	return substrates
}

// pullMode reads the plugin-pull setting (plugin-execution.md
// REQ-plugin-core-verifies): the store by default, the daemon where
// stated and the selection may run daemon images; a value naming
// neither byte path, or the daemon under a selection with no docker
// runner, is refused naming the layer the value came from.
func pullMode(v userconfig.Value, sel *runner.Selection) (daemon bool, err error) {
	switch v.Value {
	case "", pullStore:
		return false, nil
	case pullDaemon:
		if ok, why := sel.Daemon(); !ok {
			return false, v.Wrap(fmt.Errorf("plugin-pull %q: only the docker runner has a daemon to pull plugins, and %s", v.Value, why))
		}
		return true, nil
	}
	return false, v.Wrap(fmt.Errorf("plugin-pull %q names no byte path (byte paths: %s, %s)", v.Value, pullStore, pullDaemon))
}

// lazyUpdater is the update verb's plugin updater, the acquirer
// opened on the first plugin named and closed with the verb.
type lazyUpdater struct {
	settings   *userconfig.Settings
	session    *dep.Session
	acq        *oci.Acquirer
	substrates []oci.Candidate
	account    string // the selection's account, for a refusal
}

func (u *lazyUpdater) UpdatePlugin(ctx context.Context, ref string) (lockfile.PluginPin, lockfile.PluginPin, error) {
	if u.acq == nil {
		sel, err := runner.Open(nil, u.settings.Get(userconfig.KeyRunner))
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		cfg, err := acquirerConfig(u.session)
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		acq, err := oci.New(cfg)
		if err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
		u.acq = acq
		cands, account := sel.Candidates(ctx, plugin.TierNone)
		u.substrates, u.account = updateSubstrates(cands), account
	}
	before, after, err := u.acq.UpdatePlugin(ctx, ref, u.substrates)
	if errors.Is(err, oci.ErrNoCandidate) {
		err = fmt.Errorf("%w; %s", err, u.account)
	}
	return before, after, err
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
	if workDir, err = userCacheSubdir("the plugin store", "plugins"); err != nil {
		return "", "", err
	}
	if evidenceDir, err = userCacheSubdir("the plugin store", "plugin-evidence"); err != nil {
		return "", "", err
	}
	return workDir, evidenceDir, nil
}
