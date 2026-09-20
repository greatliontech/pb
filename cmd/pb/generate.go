package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/plugin/local"
	"github.com/greatliontech/pb/internal/plugin/oci"
	"github.com/greatliontech/pb/internal/plugin/runner"
	"github.com/greatliontech/pb/internal/userconfig"
	"github.com/spf13/cobra"
)

// generateCmd is the generation verb (generation.md REQ-gen-verb): the
// selected runner — the --runner flag over the runner setting, the
// environment over the user configuration file, over the platform
// default (plugin-execution.md, REQ-plugin-runner-selection) — over
// pb's plugin store, which sits beside the module cache rather than
// inside it — the module cache root holds module artifacts only
// (dep-verbs.md REQ-dep-cache-layout).
func generateCmd() *cobra.Command {
	var runnerFlag string
	var overrides []string
	cmd := &cobra.Command{
		Use: "generate", Short: "generate code from the workspace's protobuf files", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var flag *string
			if c.Flags().Changed(runner.FlagRunner) {
				flag = &runnerFlag
			}
			// The settings and the session are loaded apart here, the
			// runner between them: a runner refusal precedes a
			// workspace's.
			settings, err := userconfig.Load()
			if err != nil {
				return err
			}
			run, err := runner.Open(flag, settings.Get(userconfig.KeyRunner))
			if err != nil {
				return err
			}
			overrideMap := map[string]string{}
			for _, o := range overrides {
				ref, source, ok := strings.Cut(o, "=")
				if !ok || ref == "" || source == "" {
					return fmt.Errorf("--override %q: spelled REF=SOURCE", o)
				}
				if prev, dup := overrideMap[ref]; dup {
					return fmt.Errorf("--override %s given twice (%s and %s)", ref, prev, source)
				}
				overrideMap[ref] = source
			}
			s, err := loadSession(settings)
			if err != nil {
				return err
			}
			cfg, err := acquirerConfig(settings, run, s)
			if err != nil {
				return err
			}
			acq, err := oci.New(cfg)
			if err != nil {
				return err
			}
			defer acq.Close()
			deps := dep.GenDeps{Acquirer: acq, Runner: run, Diagnostics: os.Stderr, Overrides: overrideMap}
			// Local plugins run on the native runner wherever it exists.
			// The session's root is a path within the working tree,
			// which is rooted at the host's filesystem root.
			if native, err := runner.NativeRunner(); err == nil {
				deps.Local = &dep.LocalDeps{
					Acquirer: &local.Acquirer{Root: rootOSPath(s), Lock: s.Lock, Policy: s.Client.Policy},
					Runner:   native,
				}
			}
			return dep.Gen(c.Context(), s, deps, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&runnerFlag, runner.FlagRunner, "", "runner for oci plugins: native or docker (over "+userconfig.Keys[userconfig.KeyRunner].Env+", over the user configuration file's runner key, over the platform default)")
	cmd.Flags().StringArrayVar(&overrides, "override", nil, "REF=SOURCE: run the oci plugin REF from SOURCE for this invocation — an OCI layout directory, an OCI layout archive or docker-save tarball, or docker://IMAGE (docker runner); repeatable")
	return cmd
}
