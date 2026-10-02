package runner

import (
	"context"
	"fmt"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/userconfig"
)

// Runner names, and the flag that selects one
// (plugin-execution.md, REQ-plugin-runner-selection).
const (
	RunnerNative = "native"
	RunnerDocker = "docker"

	FlagRunner = "runner" // the --runner flag of generate
)

// The flag layer as a refusal names it; the other layers name
// themselves (userconfig.Value.From, userconfig.FromDefault).
const fromFlag = "the --" + FlagRunner + " flag"

// Candidate is a runner an entry may run on: its name as the layers
// spell it, and the runner, whose platform the entry's image must
// serve.
type Candidate struct {
	Name   string
	Runner Runner
}

// Reacher is a runner that can say, before anything runs, the
// sandbox tier its substrate reaches for an oci run: the native
// runner, whose row the host's facts select. The default consults it
// against the policy's floor.
type Reacher interface {
	Reach(ctx context.Context) (string, error)
}

// Native is the native runner's capability: a runner that says the
// tier it reaches, so the default's floor clause can never be
// skipped by a native runner that keeps it to itself.
type Native interface {
	Runner
	Reacher
}

// Selection is a run's runner selection (REQ-plugin-runner-selection):
// a layer's runner, which binds every entry of the run, or the
// platform default, which chooses per entry by capability from the
// runners the host has — the native runner where its row reaches the
// floor and the entry's image serves the host, else the docker runner
// where a daemon is reachable — and names none where neither can run
// an entry. Each runner's unavailability is kept for the refusal.
type Selection struct {
	named *Candidate
	from  string

	native    Native
	nativeErr error
	docker    Runner
	dockerErr error
}

// nativeRunner and dockerRunner are the capabilities the platform
// default turns on; tests point them elsewhere to exercise every
// default on one host.
var (
	nativeRunner = NativeRunner
	dockerRunner = func() (Runner, error) {
		r, err := NewDockerRunner("")
		if err != nil {
			return nil, err
		}
		return r, nil
	}
)

// Open applies the layers of REQ-plugin-runner-selection: the flag,
// where given (nil is not given; an empty string is given and names
// no runner), over the runner setting where the environment or the
// user configuration file stated it (user-config.md), over the
// platform default. A layer that names no runner refuses naming the
// layer and the runners; a named runner that is unavailable refuses
// naming it and its layer; the default refuses nothing here — what
// it can run is judged per entry (Candidates). Nothing is ever
// substituted.
func Open(flag *string, setting userconfig.Value) (*Selection, error) {
	name, from := "", ""
	switch {
	case flag != nil:
		name, from = *flag, fromFlag
	case setting.Stated():
		name, from = setting.Value, setting.From
	default:
		s := &Selection{from: userconfig.FromDefault}
		if s.native, s.nativeErr = nativeRunner(); s.nativeErr != nil {
			s.native = nil
		}
		if s.docker, s.dockerErr = dockerRunner(); s.dockerErr != nil {
			s.docker = nil
		}
		return s, nil
	}
	var r Runner
	var err error
	switch name {
	case RunnerNative:
		var native Native
		if native, err = nativeRunner(); err == nil {
			r = native
		}
	case RunnerDocker:
		r, err = dockerRunner()
	default:
		return nil, fmt.Errorf("runner: %q from %s names no runner (runners: %s, %s)", name, from, RunnerNative, RunnerDocker)
	}
	if err != nil {
		return nil, fmt.Errorf("runner: runner %s (from %s) is unavailable: %w", name, from, err)
	}
	return &Selection{named: &Candidate{Name: name, Runner: r}, from: from}, nil
}

// Only is the selection of one runner, which binds every entry, for
// a caller that holds the runner already: the caller is the layer.
func Only(name string, r Runner) *Selection {
	return &Selection{named: &Candidate{Name: name, Runner: r}, from: "the caller"}
}

// Candidates lists, in order of preference, the runners an entry of
// a run under the tier floor may run on, with the selection's
// account of itself for an entry no candidate serves: under a named
// runner, that one alone, the layer that named it; under the
// default, the native runner where its row reaches the floor, then
// the docker runner where a daemon is reachable — the row the host
// reaches, the floor and the daemon's reachability stated whichever
// way they fell (REQ-plugin-runner-selection: the refusal names
// them). The entry's image must serve a candidate's platform, which
// the acquisition judges in this order (REQ-plugin-platform-strict).
func (s *Selection) Candidates(ctx context.Context, floor string) (candidates []Candidate, account string) {
	if s.named != nil {
		return []Candidate{*s.named}, fmt.Sprintf("%s names runner %s for every entry", s.from, s.named.Name)
	}
	// No floor is no fact: an update offers the runners under none.
	var facts []string
	if floor != plugin.TierNone {
		facts = append(facts, "the floor "+floor)
	}
	switch {
	case s.nativeErr != nil:
		facts = append(facts, fmt.Sprintf("the native runner is unavailable (%v)", s.nativeErr))
	default:
		switch tier, err := s.native.Reach(ctx); {
		case err != nil:
			facts = append(facts, fmt.Sprintf("the native runner reaches no row (%v)", err))
		case plugin.TierBelow(tier, floor):
			facts = append(facts, fmt.Sprintf("the native runner's row reaches tier %s, below it", tier))
		default:
			meets := ", meeting it"
			if floor == plugin.TierNone {
				meets = ""
			}
			facts = append(facts, fmt.Sprintf("the native runner's row reaches tier %s%s", tier, meets))
			candidates = append(candidates, Candidate{Name: RunnerNative, Runner: s.native})
		}
	}
	if ok, why := s.Daemon(); !ok {
		facts = append(facts, why)
	} else {
		facts = append(facts, why)
		candidates = append(candidates, Candidate{Name: RunnerDocker, Runner: s.docker})
	}
	for i, f := range facts {
		if i > 0 {
			account += "; "
		}
		account += f
	}
	return candidates, account
}

// RunsDaemonImages reports whether the docker runner may run an entry
// of this selection: the layer named it, or the default has a daemon
// to turn on. A daemon-local image and the daemon byte path need it.
func (s *Selection) RunsDaemonImages() bool {
	ok, _ := s.Daemon()
	return ok
}

// Daemon reports whether the docker runner may run an entry of this
// selection, with the fact either way: the layer that named a runner,
// or the daemon's reachability under the default.
func (s *Selection) Daemon() (ok bool, why string) {
	if s.named != nil {
		if _, daemon := s.named.Runner.(DaemonImages); daemon {
			return true, fmt.Sprintf("%s names runner %s for every entry", s.from, s.named.Name)
		}
		return false, fmt.Sprintf("%s names runner %s for every entry, which runs no daemon images", s.from, s.named.Name)
	}
	if s.dockerErr != nil {
		return false, fmt.Sprintf("no daemon is reachable for the docker runner (%v)", s.dockerErr)
	}
	return true, "a daemon is reachable for the docker runner"
}
