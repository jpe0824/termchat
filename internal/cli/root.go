// Package cli builds the termchat command tree.
package cli

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is overridden at build time with -ldflags "-X .../internal/cli.Version=...".
// Without that, init takes the module version from the build (go install
// ...@v1.2.3), and falls back to "dev".
var Version = "dev"

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolveVersion(Version, info, ok)
}

// resolveVersion prefers a version set by -ldflags, then the main module's
// version from build info when it is a real one (not "(devel)"), then "dev".
func resolveVersion(ldflag string, info *debug.BuildInfo, ok bool) string {
	if ldflag != "" && ldflag != "dev" {
		return ldflag
	}
	if ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// usageError marks a mistake in how a command was invoked; it exits with 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// ExitCode is the process exit status for an error from Execute: 0 for none,
// 2 for a usage error, the command's own code for an exitError, and 1 for
// anything else.
func ExitCode(err error) int {
	var u *usageError
	var x *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &u):
		return exitUsage
	case errors.As(err, &x):
		return x.code
	}
	return 1
}

// exactArgs requires exactly n positional arguments.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usagef("%s takes %d argument(s), got %d", cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}

// noSubcommand is the Args check for a command that only groups others.
func noSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return nil
}

// groupRun is the RunE of a command that only groups others: it rejects an
// unknown subcommand and otherwise prints help.
func groupRun(cmd *cobra.Command, args []string) error {
	if err := noSubcommand(cmd, args); err != nil {
		return err
	}
	return cmd.Help()
}

// Root returns a new command tree on every call.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "termchat",
		Short:         "Terminal chat",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noSubcommand,
		RunE:          rootRun,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{msg: err.Error()}
	})
	root.AddCommand(newServeCmd(), newServerCmd(), newDaemonCmd(), newTUICmd())
	root.AddCommand(newJoinCmd(), newStatusCmd(), newChatsCmd(), newReadCmd(), newSendCmd(), newWaitCmd(),
		newSearchCmd(), newMarkReadCmd(), newDMCmd(), newRoomCmd(), newDeviceCmd(), newAgentCmd(), newInviteCmd())
	return root
}
