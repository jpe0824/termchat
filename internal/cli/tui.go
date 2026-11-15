package cli

import (
	"context"
	"errors"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/tui"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the chat interface (what termchat with no arguments does in a terminal)",
		Args:  exactArgs(0),
		RunE:  func(cmd *cobra.Command, _ []string) error { return runTUI(cmd) },
	}
}

// rootRun opens the interface when stdout is a terminal and prints help
// otherwise, so scripts and pipes see the usage text.
func rootRun(cmd *cobra.Command, args []string) error {
	if err := noSubcommand(cmd, args); err != nil {
		return err
	}
	if !isTerminal(cmd.OutOrStdout()) {
		return cmd.Help()
	}
	return runTUI(cmd)
}

// tuiDialers are the interface's two ways to connect: dial only reaches a
// running background process and is what reconnects use, so a process the
// user stopped stays stopped; start may launch one, and runs at launch and
// when the user asks.
func tuiDialers() (dial, start func(context.Context) (tui.Backend, error)) {
	wrap := func(f func(context.Context) (*ipc.Client, error)) func(context.Context) (tui.Backend, error) {
		return func(ctx context.Context) (tui.Backend, error) {
			c, err := f(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		}
	}
	return wrap(ipc.DialRunning), wrap(ipc.Dial)
}

// runTUI runs the interface until the user quits. The program starts at
// once; the first connection is made inside it. Tests replace it.
var runTUI = func(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	dial, start := tuiDialers()
	var p *tea.Program
	m := tui.New(nil, tui.Options{
		Splash:    cfg.Splash,
		MarkRead:  cfg.MarkRead,
		PasteGap:  pasteGap(cfg.PasteGapMS),
		NotifyOSC: cfg.Notify.OSC,
		Version:   Version,
		Server:    configuredServer,
		Env:       os.Getenv,
		Dial:      dial,
		Start:     start,
		Send:      func(msg tea.Msg) { p.Send(msg) },
	})
	defer m.Close()
	p = tea.NewProgram(m, tea.WithContext(cmdContext(cmd)),
		tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	_, err = p.Run()
	return tuiExit(err)
}

// tuiExit words the end of the interface. On a panic the framework has
// already restored the terminal and printed the panic and its stack; this
// adds the one thing the user needs to know.
func tuiExit(err error) error {
	if errors.Is(err, tea.ErrProgramPanic) {
		return errors.New("the chat interface stopped on an internal error; unsent drafts in it were lost")
	}
	return err
}

// configuredServer is the first account's server address, re-read so an
// account joined while the interface runs is shown.
func configuredServer() string {
	cfg, err := config.Load()
	if err != nil || len(cfg.Accounts) == 0 {
		return ""
	}
	return cfg.Accounts[0].Server
}

// pasteGap turns config paste_gap_ms into the interface's option, where a
// negative value turns the wait off.
func pasteGap(ms int) time.Duration {
	if ms <= 0 {
		return -1
	}
	return time.Duration(ms) * time.Millisecond
}
