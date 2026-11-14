// Package cli builds the termchat command tree.
package cli

import "github.com/spf13/cobra"

// Version is overridden at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// Root returns a new command tree on every call.
func Root() *cobra.Command {
	return &cobra.Command{
		Use:           "termchat",
		Short:         "Terminal chat",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}
