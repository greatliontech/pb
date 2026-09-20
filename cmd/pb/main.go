// Command pb is the protobuf module manager. This layer is assembly
// only: it reads the environment, wires the seams, and hands every
// observable behavior to the verb layer (internal/dep), where it lives
// under test.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pb:", err)
		os.Exit(1)
	}
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
	return root
}
