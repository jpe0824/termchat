package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/server"
)

const (
	dataEnv     = "TERMCHAT_SERVER_DATA"
	defaultAddr = ":7420"
	purgeEvery  = time.Hour
)

// addDataFlag registers --data on cmd; persistent makes subcommands inherit it.
func addDataFlag(cmd *cobra.Command, persistent bool) {
	flags := cmd.Flags()
	if persistent {
		flags = cmd.PersistentFlags()
	}
	flags.String("data", "", "server data directory (or set "+dataEnv+")")
}

// dataDir resolves the data directory from --data or the environment. There
// is no default, so a server never starts on a directory by accident.
func dataDir(cmd *cobra.Command) (string, error) {
	dir, err := cmd.Flags().GetString("data")
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = os.Getenv(dataEnv)
	}
	if dir == "" {
		return "", usagef("--data <dir> is required (or set %s)", dataEnv)
	}
	return dir, nil
}

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the chat server",
		Args:  noSubcommand,
		RunE:  runServe,
	}
	addDataFlag(cmd, false)
	cmd.Flags().String("addr", defaultAddr, "address to listen on")
	cmd.Flags().Duration("retention", 0, "erase message bodies older than this; 0 keeps everything")
	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	dir, err := dataDir(cmd)
	if err != nil {
		return err
	}
	addr, _ := cmd.Flags().GetString("addr")
	retention, _ := cmd.Flags().GetDuration("retention")
	if retention < 0 {
		return usagef("--retention must not be negative")
	}

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	srv, err := server.New(server.Options{Addr: addr, DataDir: dir, Logger: logger})
	if err != nil {
		return err
	}

	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "listening on %s\n", srv.Addr())
	fmt.Fprintf(out, "host key %s\n", ssh.FingerprintSHA256(srv.HostKey()))

	var purger sync.WaitGroup
	if retention > 0 {
		purger.Add(1)
		go func() {
			defer purger.Done()
			purgeLoop(ctx, srv.Store(), retention, purgeEvery, logger)
		}()
	}
	err = srv.Serve(ctx)
	stop()
	purger.Wait()
	return err
}

// purgeLoop erases old message bodies once immediately and then every period
// until ctx is done.
func purgeLoop(ctx context.Context, st *server.Store, retention, period time.Duration, log *slog.Logger) {
	run := func() {
		n, err := st.PurgeOlderThan(retention)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("retention purge failed", "err", err)
			}
			return
		}
		log.Info("retention purge", "erased", n, "older_than", retention)
	}
	run()
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
