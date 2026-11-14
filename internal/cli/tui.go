package cli

import (
	"context"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/tui"
)

// tuiDialWait bounds the first connection attempt; the interface opens
// either way and keeps trying in the background.
const tuiDialWait = 5 * time.Second

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

// runTUI runs the interface until the user quits. Tests replace it.
var runTUI = func(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	ctx := cmdContext(cmd)
	dial := func(ctx context.Context) (tui.Backend, error) {
		c, err := ipc.Dial(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	first, cancel := context.WithTimeout(ctx, tuiDialWait)
	b, _ := dial(first)
	cancel()

	var p *tea.Program
	m := tui.New(b, tui.Options{
		Splash:  cfg.Splash,
		Version: Version,
		Server:  configuredServer,
		Env:     os.Getenv,
		Dial:    dial,
		Send:    func(msg tea.Msg) { p.Send(msg) },
	})
	defer m.Close()
	p = tea.NewProgram(m, tea.WithContext(ctx),
		tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	_, err = p.Run()
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
