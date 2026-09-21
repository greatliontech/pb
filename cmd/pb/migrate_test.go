package main

import (
	"errors"
	"testing"

	"github.com/greatliontech/pb/internal/migrate"
)

// The migrate verb is registered with its flags — the module path,
// the repeatable replacements — and its unmapped status exits 1 with
// no message of its own, the report speaking for it (migrate.md
// REQ-migrate-verb, REQ-migrate-report).
func TestMigrateCommand(t *testing.T) {
	root := rootCmd()
	cmd, _, err := root.Find([]string{"migrate"})
	if err != nil || cmd.Name() != "migrate" {
		t.Fatalf("migrate: %v", err)
	}
	for _, flag := range []string{"module", "dep", "plugin"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("no --%s flag", flag)
		}
	}
	if err := cmd.Flags().Parse([]string{"--dep", "a=b", "--dep", "c=d", "--plugin", "p=q", "--module", "m"}); err != nil {
		t.Fatal(err)
	}
	if deps, _ := cmd.Flags().GetStringArray("dep"); len(deps) != 2 {
		t.Errorf("--dep not repeatable: %v", deps)
	}
	if got := failure(migrate.ErrUnmapped); got != "" {
		t.Errorf("an unmapped status said %q", got)
	}
	if got := failure(errors.New("x")); got != "pb: x" {
		t.Errorf("a failure said %q", got)
	}
}
