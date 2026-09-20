// Package rapidtest is the one home of the rapid-oracle discipline:
// property tests double as mutation-testing oracles, and an oracle must
// be deterministic and side-effect-free. Main pins rapid's PRNG seed
// (unless the caller set one explicitly, by flag or by rapid's
// RAPID_SEED environment default) and disables its failure-file
// persistence; with the seed fixed, a failure replays by re-running the
// test.
//
// The pin trades exploration for determinism: a local run, a spec
// check, a mutation probe all walk one trajectory. Fresh examples come
// from a run that sets the seed, which is what CI's varying-seed leg
// does with the run id.
package rapidtest

import (
	"flag"
	"os"
	"testing"
)

// Main is the shared TestMain body for packages with rapid properties:
//
//	func TestMain(m *testing.M) { rapidtest.Main(m) }
func Main(m *testing.M) {
	Pin()
	os.Exit(m.Run())
}

// Pin applies the discipline without running the tests, for a package
// whose TestMain has work of its own before m.Run: rapid's seed fixed
// unless the caller set one, its failure files off. Parses the flags
// where nothing has yet.
func Pin() {
	if !flag.Parsed() {
		flag.Parse()
	}
	if f := flag.Lookup("rapid.seed"); f != nil && f.Value.String() == "0" {
		if err := flag.Set("rapid.seed", "1"); err != nil {
			panic(err)
		}
	}
	if flag.Lookup("rapid.nofailfile") != nil {
		if err := flag.Set("rapid.nofailfile", "true"); err != nil {
			panic(err)
		}
	}
}
