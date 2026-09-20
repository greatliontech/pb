package rapidtest

import (
	"flag"
	"testing"

	// The flags Pin sets are rapid's, defined by linking it.
	_ "pgregory.net/rapid"
)

// Pin fixes the seed only where none was chosen: rapid's zero, the
// random seed, becomes one; a seed the run set, by flag or by the
// environment default rapid reads into the flag, survives. The failure
// files are off either way.
func TestPinKeepsAChosenSeed(t *testing.T) {
	seed, files := flag.Lookup("rapid.seed"), flag.Lookup("rapid.nofailfile")
	// The flags are the process's; the run's own values come back.
	for _, f := range []*flag.Flag{seed, files} {
		was := f.Value.String()
		t.Cleanup(func() {
			if err := flag.Set(f.Name, was); err != nil {
				t.Error(err)
			}
		})
	}
	for _, c := range []struct{ before, after string }{{"0", "1"}, {"7", "7"}, {"1", "1"}} {
		if err := flag.Set("rapid.seed", c.before); err != nil {
			t.Fatal(err)
		}
		if err := flag.Set("rapid.nofailfile", "false"); err != nil {
			t.Fatal(err)
		}
		Pin()
		if got := seed.Value.String(); got != c.after {
			t.Errorf("seed %s pinned to %s, want %s", c.before, got, c.after)
		}
		if got := files.Value.String(); got != "true" {
			t.Errorf("seed %s: failure files %s, want off", c.before, got)
		}
	}
}
