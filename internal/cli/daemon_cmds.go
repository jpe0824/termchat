package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/ipc"
)

// errNoDaemon is what Dial reports for "stop" when nothing is listening: the
// command must not start a process just to stop it.
var errNoDaemon = errors.New("no background process is running")

// stopWait bounds how long "daemon stop" waits for the process to go away.
const stopWait = 10 * time.Second

func newDaemonStartCmd() *cobra.Command {
	return clientCmd(&cobra.Command{
		Use:   "start",
		Short: "Make sure the background process is running",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(c *ipc.Client) error {
				s, err := c.Status()
				if err != nil {
					return err
				}
				return emit(cmd, statusOf(s), func(w io.Writer) error {
					state := "no account"
					if s.Account != "" {
						state = "account @" + cell(s.Me.Handle) + ", " + cell(string(s.Conn))
					}
					_, err := fmt.Fprintf(w, "background process running (%s)\n", state)
					return err
				})
			})
		},
	})
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the background process",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fail(stopDaemon(cmdContext(cmd)))
		},
	}
}

// stopDaemon asks the process to stop and waits until nothing answers any
// more. It succeeds when none is running.
func stopDaemon(ctx context.Context) error {
	ipc.StartDaemon = func(context.Context) error { return errNoDaemon }
	deadline := time.Now().Add(stopWait)
	asked := false
	for {
		c, err := ipc.Dial(ctx)
		switch {
		case errors.Is(err, errNoDaemon):
			return nil
		case err == nil:
			if !asked {
				asked = true
				if err := c.Stop(); err != nil && !errors.Is(err, ipc.ErrShuttingDown) && !errors.Is(err, ipc.ErrClosed) {
					_ = c.Close()
					return err
				}
			}
			_ = c.Close()
		case !errors.Is(err, ipc.ErrShuttingDown):
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("the background process did not stop in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
