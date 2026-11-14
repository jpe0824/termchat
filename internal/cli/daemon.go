package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/daemon"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background process",
		Args:  noSubcommand,
		RunE:  groupRun,
	}
	cmd.AddCommand(&cobra.Command{
		Use:    "run",
		Hidden: true,
		Short:  "Run the background process in the foreground",
		Args:   noSubcommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, daemon.Options{})
		},
	})
	cmd.AddCommand(newDaemonStartCmd(), newDaemonStopCmd())
	return cmd
}
