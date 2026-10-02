package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/lsp"
	"github.com/greatliontech/pb/internal/userconfig"
)

// lspCmd is the language server's verb (lsp.md): one client served
// over standard input and output, the session of the client's root
// loaded read-only as a check verb loads its own, the process ending
// with the connection — status 0 after the client's shutdown, 1
// without it.
func lspCmd() *cobra.Command {
	return &cobra.Command{
		Use: "lsp", Short: "serve an editor over the Language Server Protocol on standard input and output", Args: cobra.NoArgs,
		Long: `pb lsp serves one editor over the Language Server Protocol, speaking on
standard input and output and logging on standard error. The session is
the client root's — its first workspace folder — loaded as pb lint loads
its own but read-only: the lockfile's pins are honoured and never
written, and a requirement the lockfile does not pin is diagnosed naming
pb dep download. Open files are judged in place of the tree's, and the
compile's errors and pb lint's findings are published as diagnostics.`,
		RunE: func(c *cobra.Command, _ []string) error {
			settings, err := userconfig.Load()
			if err != nil {
				return err
			}
			client, err := assembleClient(settings)
			if err != nil {
				return err
			}
			sources, err := sourcesStoreDir()
			if err != nil {
				return err
			}
			// The tree is rooted at the client root's volume, as the
			// verbs root theirs at the working directory's, so the
			// client root is any directory of it.
			srv, err := lsp.New(lsp.Deps{
				OpenTree: func(root string) billy.Filesystem { return osfs.New(root) },
				Client:   client,
				Sources:  sources,
				Logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
			})
			if err != nil {
				return err
			}
			status := lsp.Serve(c.Context(), srv, os.Stdin, os.Stdout)
			if status != 0 {
				return exitStatus(status)
			}
			return nil
		},
	}
}

// exitStatus is the verb's failing status without a message: the
// connection ended without the client's shutdown.
type exitStatus int

func (e exitStatus) Error() string {
	return fmt.Sprintf("the connection ended without shutdown (status %d)", int(e))
}
