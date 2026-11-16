package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

const configSource = "cli"

const roomsOnly = "notification modes apply to rooms only"

func newConfigCmd() *cobra.Command {
	list := clientCmd(&cobra.Command{
		Use:   "config",
		Short: "List, read and change settings",
		Args:  noSubcommand,
		RunE:  configList,
	})
	get := clientCmd(&cobra.Command{
		Use:   "get <key>",
		Short: "Print one setting (rooms.<room> for a room's notification mode)",
		Args:  exactArgs(1),
		RunE:  configGet,
	})
	set := clientCmd(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change a setting; it applies at once",
		Args:  exactArgs(2),
		RunE:  configSet,
	})
	list.AddCommand(get, set)
	return list
}

func configList(cmd *cobra.Command, _ []string) error {
	return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
		settings, err := c.ConfigGet()
		if err != nil {
			return err
		}
		return emit(cmd, settings, func(w io.Writer) error {
			tw := table(w)
			fmt.Fprintln(tw, "KEY\tVALUE\tAPPLIES")
			for _, s := range settings {
				key := s.Key
				if s.Room != "" {
					key = config.RoomPrefix + s.Room
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", cell(key), cell(s.Value), cell(string(s.Applies)))
			}
			return tw.Flush()
		})
	})
}

// memberRoom resolves ref to a room the account has joined. A room that
// exists but is not joined exits 3 naming it, the same as an unknown chat.
func memberRoom(c *ipc.Client, ref string) (model.Conversation, error) {
	conv, err := resolveChat(c, ref)
	if err != nil {
		return model.Conversation{}, err
	}
	if conv.Kind != model.ConvRoom {
		return model.Conversation{}, usagef("%s", roomsOnly)
	}
	mine, _, err := c.Chats()
	if err != nil {
		return model.Conversation{}, err
	}
	if !slices.ContainsFunc(mine, func(v ipc.ConvView) bool { return v.ID == conv.ID }) {
		return model.Conversation{}, exitf(exitNotFound, "not a member of #"+conv.Name)
	}
	return conv, nil
}

// roomKey turns rooms.<name> into rooms.<id>. Other keys pass unchanged.
func roomKey(c *ipc.Client, key string) (string, error) {
	ref, ok := strings.CutPrefix(key, config.RoomPrefix)
	if !ok {
		return key, nil
	}
	conv, err := memberRoom(c, ref)
	if err != nil {
		return "", err
	}
	return config.RoomPrefix + conv.ID, nil
}

func configGet(cmd *cobra.Command, args []string) error {
	return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
		key, err := roomKey(c, args[0])
		if err != nil {
			return err
		}
		settings, err := c.ConfigGet()
		if err != nil {
			return err
		}
		for _, s := range settings {
			if s.Key == key {
				return printSetting(cmd, s, func(w io.Writer) error {
					_, err := fmt.Fprintln(w, cell(s.Value))
					return err
				})
			}
		}
		return usagef("%s: unknown setting", sanitize.Line(args[0]))
	})
}

func configSet(cmd *cobra.Command, args []string) error {
	return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
		key, err := roomKey(c, args[0])
		if err != nil {
			return err
		}
		if err := config.Validate(key, args[1]); err != nil {
			return usagef("%s", sanitize.Line(err.Error()))
		}
		s, err := c.ConfigSet(key, args[1], configSource)
		if err != nil {
			return err
		}
		return printSetting(cmd, s, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "%s = %s\n", cell(args[0]), cell(s.Value))
			return err
		})
	})
}

// printSetting prints one setting as plain text, or as JSON when --json is
// given explicitly: a script reading one value wants the value, not JSON.
func printSetting(cmd *cobra.Command, s ipc.Setting, text func(w io.Writer) error) error {
	if j, _ := cmd.Flags().GetBool("json"); j {
		return emit(cmd, s, nil)
	}
	return text(cmd.OutOrStdout())
}
