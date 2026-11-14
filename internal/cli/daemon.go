package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/daemon"
	"github.com/jpe0824/termchat/internal/notify"
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
			ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, daemon.Options{
				NewNotifier: func(cfg *config.Config, log *slog.Logger) daemon.Notifier { return notify.New(cfg, log) },
			})
		},
	})
	cmd.AddCommand(newDaemonStartCmd(), newDaemonStopCmd())
	return cmd
}
