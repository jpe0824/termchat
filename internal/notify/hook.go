package notify

import (
	"context"
	"os"
	"os/exec"
)

// hookCommand is the hook run through the platform shell, with p in its
// environment.
func hookCommand(hook string, p Notification) command {
	name, args := shell(hook)
	return command{Name: name, Args: args, Env: []string{
		"TERMCHAT_CONV=" + p.ConvID,
		"TERMCHAT_CONV_NAME=" + p.ConvName,
		"TERMCHAT_SENDER=" + p.Sender,
		"TERMCHAT_TEXT=" + p.Text,
		"TERMCHAT_MENTION=" + flag(p.Mention),
		"TERMCHAT_DM=" + flag(p.DM),
	}}
}

func flag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// execRun runs c with its stdin closed and its output discarded. When ctx
// ends the process and its children are killed.
func execRun(ctx context.Context, c command) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append(os.Environ(), c.Env...)
	killTree(cmd)
	return cmd.Run()
}
