// Package rapidtest is the one home of the rapid-oracle discipline:
// property tests double as mutation-testing oracles, and an oracle must
// be deterministic and side-effect-free. Main pins rapid's PRNG seed
// (unless the caller set one explicitly) and disables its failure-file
// persistence; with the seed fixed, a failure replays by re-running the
// test.
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
	flag.Parse()
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
	os.Exit(m.Run())
}
