// Command pb is the protobuf module manager. This layer is assembly
// only: it reads the environment, wires the seams, and hands every
// observable behavior to the verb layer (internal/dep), where it lives
// under test.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/migrate"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		if msg := failure(err); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}
}

// failure is what a failed run says on standard error before exiting
// 1: the cause, prefixed — or nothing for a check verb's failing
// status, spoken for by the findings it printed (check-rules.md
// REQ-check-exit-status), or the format verb's under --exit-code
// (format.md REQ-format-verb).
func failure(err error) string {
	if errors.Is(err, dep.ErrFindings) || errors.Is(err, dep.ErrUnformatted) || errors.Is(err, migrate.ErrUnmapped) {
		return ""
	}
	return "pb: " + err.Error()
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pb",
		Short:         "protobuf module manager",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(depCmd())
	root.AddCommand(generateCmd())
	root.AddCommand(exportCmd())
	root.AddCommand(buildCmd())
	root.AddCommand(cleanCmd())
	root.AddCommand(pluginCmd())
	root.AddCommand(lintCmd())
	root.AddCommand(breakingCmd())
	root.AddCommand(migrateCmd())
	root.AddCommand(formatCmd())
	return root
}
