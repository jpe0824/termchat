package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// shutdownRetryDelay is how long a command waits for a background process
// that is on its way down before it dials again.
const shutdownRetryDelay = 500 * time.Millisecond

// clientCmd adds the flags every client command takes.
func clientCmd(c *cobra.Command) *cobra.Command {
	c.Flags().String("account", "", "account to use (default: the only account)")
	c.Flags().Bool("json", false, "print JSON (the default when stdout is not a terminal)")
	return c
}

func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// checkAccount rejects an --account other than the configured one. This
// version has a single account, so an omitted flag means that one.
func checkAccount(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("account")
	if name == "" {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Accounts) == 0 {
		return nil
	}
	for _, a := range cfg.Accounts {
		if a.Name == name {
			return nil
		}
	}
	return usagef("unknown account %q; the configured account is %q", name, cfg.Accounts[0].Name)
}

// withClient runs fn against the background process, starting it when none
// is running. A process that is shutting down ran nothing, so after a short
// wait the command is repeated once against a fresh connection. Errors come
// back mapped by fail.
func withClient(cmd *cobra.Command, fn func(c *ipc.Client) error) error {
	return withClientCtx(cmd, cmdContext(cmd), fn)
}

// withClientCtx is withClient with ctx bounding the dial.
func withClientCtx(cmd *cobra.Command, ctx context.Context, fn func(c *ipc.Client) error) error {
	if err := checkAccount(cmd); err != nil {
		return fail(err)
	}
	attempt := func() error {
		c, err := ipc.Dial(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		return fn(c)
	}
	err := attempt()
	if errors.Is(err, ipc.ErrShuttingDown) {
		time.Sleep(shutdownRetryDelay)
		err = attempt()
	}
	return fail(err)
}

// argRange requires between lo and hi positional arguments.
func argRange(lo, hi int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) < lo:
			return usagef("%s takes at least %d argument(s), got %d", cmd.CommandPath(), lo, len(args))
		case len(args) > hi:
			return usagef("%s takes at most %d argument(s), got %d", cmd.CommandPath(), hi, len(args))
		}
		return nil
	}
}

const nameRule = "use lowercase letters, digits, - and _, starting with a letter or digit, at most 32 characters"

// handleArg normalises a handle given on the command line.
func handleArg(s string) (string, error) {
	h := model.NormalizeName(s)
	if !model.ValidName(h) {
		return "", usagef("invalid handle %q: %s", s, nameRule)
	}
	return h, nil
}

// roomArg normalises a room name given on the command line.
func roomArg(s string) (string, error) {
	n := model.NormalizeName(s)
	if !model.ValidName(n) {
		return "", usagef("invalid room name %q: %s", s, nameRule)
	}
	return n, nil
}

// whoAmI returns the signed-in handle, or ErrNoAccount.
func whoAmI(c *ipc.Client) (string, error) {
	s, err := c.Status()
	if err != nil {
		return "", err
	}
	if s.Account == "" {
		return "", ipc.ErrNoAccount
	}
	return s.Me.Handle, nil
}

func newJoinCmd() *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   "join <host:port> --invite <code>",
		Short: "Join a server with an invite code",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, _ := cmd.Flags().GetString("invite")
			if code == "" {
				return usagef("--invite is required")
			}
			return withClient(cmd, func(c *ipc.Client) error {
				if err := c.Join(args[0], code); err != nil {
					return err
				}
				s, err := c.Status()
				if err != nil {
					return err
				}
				return emit(cmd, statusOf(s), func(w io.Writer) error {
					_, err := fmt.Fprintf(w, "joined %s as @%s\n", cell(args[0]), cell(s.Me.Handle))
					return err
				})
			})
		},
	})
	cmd.Flags().String("invite", "", "invite code")
	return cmd
}

func newStatusCmd() *cobra.Command {
	return clientCmd(&cobra.Command{
		Use:   "status",
		Short: "Show the account and connection state",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(c *ipc.Client) error {
				s, err := c.Status()
				if err != nil {
					return err
				}
				if s.Account == "" {
					return ipc.ErrNoAccount
				}
				return emit(cmd, statusOf(s), func(w io.Writer) error {
					return printStatus(w, statusOf(s))
				})
			})
		},
	})
}

func printStatus(w io.Writer, s statusJSON) error {
	syncing := "no"
	if s.Syncing {
		syncing = "yes"
	}
	tw := table(w)
	fmt.Fprintf(tw, "account\t%s\n", cell(s.Account))
	fmt.Fprintf(tw, "handle\t@%s\n", cell(s.Handle))
	fmt.Fprintf(tw, "state\t%s\n", cell(s.State))
	fmt.Fprintf(tw, "pending\t%d\n", s.Pending)
	fmt.Fprintf(tw, "syncing\t%s\n", syncing)
	return tw.Flush()
}

// joinCommand is the command that adds another device or person on the
// account's server.
func joinCommand(code string) string {
	server := "<host:port>"
	if cfg, err := config.Load(); err == nil && len(cfg.Accounts) > 0 && cfg.Accounts[0].Server != "" {
		server = cfg.Accounts[0].Server
	}
	return fmt.Sprintf("termchat join %s --invite %s", server, strings.TrimSpace(code))
}

// resolveChat finds the conversation a <chat> argument names.
func resolveChat(c *ipc.Client, ref string) (model.Conversation, error) {
	if strings.TrimSpace(ref) == "" {
		return model.Conversation{}, usagef("the chat argument is empty")
	}
	conv, err := c.Resolve(ref)
	if errors.Is(err, ipc.ErrNotFound) {
		return model.Conversation{}, exitf(exitNotFound, "no such chat: "+ref)
	}
	return conv, err
}

// convLabels maps conversation ids to their names with sigils, for tables.
func convLabels(c *ipc.Client, me string) (map[string]string, error) {
	mine, open, err := c.Chats()
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for _, v := range mine {
		labels[v.ID] = convLabel(v.Conversation, me)
	}
	for _, o := range open {
		labels[o.ID] = convLabel(o, me)
	}
	return labels, nil
}

func newChatsCmd() *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   "chats",
		Short: "List conversations with unread counts, most recent first",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, _ := cmd.Flags().GetBool("all")
			return withClient(cmd, func(c *ipc.Client) error {
				me, err := whoAmI(c)
				if err != nil {
					return err
				}
				mine, open, err := c.Chats()
				if err != nil {
					return err
				}
				slices.SortStableFunc(mine, func(a, b store.ConvView) int { return b.LastAt.Compare(a.LastAt) })
				list := make([]chatJSON, 0, len(mine)+len(open))
				for _, v := range mine {
					j := chatJSON{ID: v.ID, Kind: string(v.Kind), Name: convName(v.Conversation, me), Unread: v.Unread}
					if !v.LastAt.IsZero() {
						at := rfc3339(v.LastAt)
						j.LastAt = &at
					}
					list = append(list, j)
				}
				if all {
					notJoined := false
					for _, o := range open {
						list = append(list, chatJSON{ID: o.ID, Kind: string(o.Kind), Name: convName(o, me), Joined: &notJoined})
					}
				}
				return emit(cmd, list, func(w io.Writer) error {
					tw := table(w)
					for _, j := range list {
						label := "#" + cell(j.Name)
						if j.Kind == string(model.ConvDM) {
							label = "@" + cell(j.Name)
						}
						last, note := "-", ""
						if j.LastAt != nil {
							last = *j.LastAt
						}
						if j.Joined != nil {
							note = "(not joined)"
						}
						fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", label, j.Unread, last, note)
					}
					return tw.Flush()
				})
			})
		},
	})
	cmd.Flags().Bool("all", false, "also list open rooms you have not joined")
	return cmd
}

func newMarkReadCmd() *cobra.Command {
	return clientCmd(&cobra.Command{
		Use:   "mark-read <chat>",
		Short: "Mark a conversation read up to its newest message",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(c *ipc.Client) error {
				conv, err := resolveChat(c, args[0])
				if err != nil {
					return err
				}
				return c.MarkRead(conv.ID)
			})
		},
	})
}

func newDMCmd() *cobra.Command {
	return clientCmd(&cobra.Command{
		Use:   "dm <handle>",
		Short: "Open or create the direct conversation with a person and print its id",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := handleArg(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(c *ipc.Client) error {
				me, err := whoAmI(c)
				if err != nil {
					return err
				}
				conv, err := c.OpenDM(handle)
				if errors.Is(err, ipc.ErrNotFound) {
					return exitf(exitNotFound, "no such person: "+handle)
				}
				if err != nil {
					return err
				}
				return emit(cmd, convOf(conv, me), func(w io.Writer) error {
					_, err := fmt.Fprintln(w, cell(conv.ID))
					return err
				})
			})
		},
	})
}

// inviteCmd builds a command that asks the server for an invite code. who
// says in the table who runs the printed join command.
func inviteCmd(use, short, who string, args cobra.PositionalArgs, call func(c *ipc.Client, args []string, name string) (string, error), named bool) *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			return withClient(cmd, func(c *ipc.Client) error {
				code, err := call(c, args, name)
				if err != nil {
					return err
				}
				j := codeJSON{Code: code, Join: joinCommand(code)}
				return emit(cmd, j, func(w io.Writer) error {
					fmt.Fprintf(w, "invite code: %s\n%s\n  %s\n", cell(j.Code), who, cell(j.Join))
					return nil
				})
			})
		},
	})
	if named {
		cmd.Flags().String("name", "", "display name")
	}
	return cmd
}

func newDeviceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "device", Short: "Manage your devices", Args: noSubcommand, RunE: groupRun}
	cmd.AddCommand(inviteCmd("add", "Print an invite code for another device of yours",
		"on the other device, run:", exactArgs(0),
		func(c *ipc.Client, _ []string, _ string) (string, error) { return c.DeviceInvite() }, false))
	return cmd
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Manage your agents", Args: noSubcommand, RunE: groupRun}
	cmd.AddCommand(inviteCmd("create <handle>", "Create an agent identity you own and print its invite code",
		"on the machine the agent runs on, run:", exactArgs(1),
		func(c *ipc.Client, args []string, name string) (string, error) {
			handle, err := handleArg(args[0])
			if err != nil {
				return "", err
			}
			return c.CreateAgent(handle, name)
		}, true))
	return cmd
}

func newInviteCmd() *cobra.Command {
	return inviteCmd("invite <handle>", "Invite a person to the server (admins only)",
		"the person you invited runs:", exactArgs(1),
		func(c *ipc.Client, args []string, name string) (string, error) {
			handle, err := handleArg(args[0])
			if err != nil {
				return "", err
			}
			return c.AdminInvite(handle, name)
		}, true)
}
