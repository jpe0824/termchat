package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// printMessages writes messages as a JSON array (one object when single),
// or as table lines. labels, when set, names each message's conversation.
// failures maps ids of the user's failed outbox items to their reason.
func printMessages(cmd *cobra.Command, ms []ipc.Message, failures, labels map[string]string, single bool) error {
	list := msgsOf(ms, failures)
	var v any = list
	if single {
		v = list[0]
	}
	return emit(cmd, v, func(w io.Writer) error {
		for i, m := range list {
			prefix := ""
			if labels != nil {
				label, ok := labels[ms[i].ConvID]
				if !ok {
					label = ms[i].ConvID
				}
				prefix = cell(label) + "  "
			}
			writeMessage(w, m, prefix)
		}
		return nil
	})
}

func newReadCmd() *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   "read <chat>",
		Short: "Show a conversation's messages, oldest to newest, without marking them read",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, _ := cmd.Flags().GetInt("limit")
			before, _ := cmd.Flags().GetInt64("before")
			if n < 1 {
				return usagef("-n must be at least 1")
			}
			if before < 0 {
				return usagef("--before must not be negative")
			}
			return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
				conv, err := resolveChat(c, args[0])
				if err != nil {
					return err
				}
				ms, err := c.Messages(conv.ID, before, n)
				if err != nil {
					return err
				}
				var failures map[string]string
				for _, m := range ms {
					if m.Status == ipc.MsgFailed {
						if failures, err = failedReasons(c, conv.ID); err != nil {
							return err
						}
						break
					}
				}
				return printMessages(cmd, ms, failures, nil, false)
			})
		},
	})
	cmd.Flags().IntP("limit", "n", 20, "how many messages")
	cmd.Flags().Int64("before", 0, "only messages with a sequence number below this")
	return cmd
}

func failedReasons(c *ipc.Client, conv string) (map[string]string, error) {
	items, err := c.Outbox(conv)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, it := range items {
		if it.State == ipc.MsgFailed {
			out[it.MsgID] = it.Err
		}
	}
	return out, nil
}

func newSearchCmd() *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   "search <query>",
		Short: "Find messages containing every word of the query, newest first",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usagef("%s needs a query", cmd.CommandPath())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			n, _ := cmd.Flags().GetInt("limit")
			if n < 1 {
				return usagef("-n must be at least 1")
			}
			return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
				me, err := whoAmI(c)
				if err != nil {
					return err
				}
				ms, err := c.Search(strings.Join(args, " "), n)
				if err != nil {
					return err
				}
				labels, err := convLabels(c, me)
				if err != nil {
					return err
				}
				return printMessages(cmd, ms, nil, labels, false)
			})
		},
	})
	cmd.Flags().IntP("limit", "n", 20, "how many messages")
	return cmd
}

// messageText returns what to send: the argument, or all of stdin when the
// argument is absent or "-", minus one trailing newline.
func messageText(cmd *cobra.Command, args []string) (string, error) {
	var text string
	if len(args) == 2 && args[1] != "-" {
		text = args[1]
	} else {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read the message from stdin: %w", err)
		}
		text = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	}
	switch model.CheckText(text) {
	case model.ErrEmptyText:
		return "", usagef("no message text; give it as an argument or on stdin")
	case model.ErrTextTooLong:
		return "", usagef("the message is %d bytes; the limit is %d", len(text), model.MaxTextBytes)
	}
	return text, nil
}

func newSendCmd() *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   "send <chat> [text]",
		Short: "Queue a message; the text comes from stdin when omitted or \"-\"",
		Long: `Queue a message for <chat> and return at once with its id and state "sending".

Everything after <chat> is the text, even when it starts with "-", so flags
go before the chat: termchat send --json @bob "- done". With no text, or "-",
the text is read from standard input (one trailing newline is dropped). "--"
before the chat also works.`,
		Args: argRange(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := messageText(cmd, args)
			if err != nil {
				return err
			}
			return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
				conv, err := resolveChat(c, args[0])
				if err != nil {
					return err
				}
				id, err := c.Send(conv.ID, text, "cli")
				if err != nil {
					return err
				}
				res := struct {
					MsgID string `json:"msg_id"`
					State string `json:"state"`
				}{id, ipc.MsgSending}
				return emit(cmd, res, func(w io.Writer) error {
					_, err := fmt.Fprintf(w, "queued %s (%s)\n", cell(id), res.State)
					return err
				})
			})
		},
	})
	// Text may start with "-", so flag parsing stops at the chat.
	cmd.Flags().SetInterspersed(false)
	return cmd
}
