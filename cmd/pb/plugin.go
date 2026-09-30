package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/plugin/publish"
)

// pluginCmd groups the verbs over plugin images; build publishes one
// (plugin-publish.md REQ-publish-verb).
func pluginCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plugin", Short: "plugin image operations"}
	var entrypoint, executables, platforms, bases []string
	build := &cobra.Command{
		Use: "build <reference>", Short: "package platform trees as a plugin image and publish it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			req, err := publish.ParseRequest(args[0], entrypoint, executables, platforms, bases)
			if err != nil {
				return fmt.Errorf("plugin build: %w", err)
			}
			out, err := publish.Build(c.Context(), req)
			if err != nil {
				return err
			}
			w := c.OutOrStdout()
			if out.Unchanged {
				fmt.Fprintf(w, "%s@%s unchanged\n", out.Reference, out.Digest)
			} else {
				fmt.Fprintf(w, "%s@%s published\n", out.Reference, out.Digest)
			}
			for _, img := range out.Images {
				fmt.Fprintf(w, "  %s %s\n", img.Platform, img.Digest)
			}
			return nil
		},
	}
	build.Flags().StringArrayVar(&entrypoint, "entrypoint", nil, "the plugin process's argv, its first value an absolute path within every platform tree; repeatable, in order")
	build.Flags().StringArrayVar(&executables, "executable", nil, "an absolute path within every platform tree the image marks executable beside the entrypoint; repeatable")
	build.Flags().StringArrayVar(&platforms, "platform", nil, "<os>/<arch>=<directory>: a platform and the tree that is its image's files; repeatable")
	build.Flags().StringArrayVar(&bases, "base", nil, "<os>/<arch>=<reference>@<digest>: the base image a platform's image is built over; repeatable")
	cmd.AddCommand(build)
	return cmd
}
