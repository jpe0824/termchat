package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

func newRoomCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "room", Short: "Create and manage rooms", Args: noSubcommand, RunE: groupRun}

	create := clientCmd(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a room (open to everyone unless --private)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := roomArg(args[0])
			if err != nil {
				return err
			}
			private, _ := cmd.Flags().GetBool("private")
			return roomOp(cmd, "", func(*ipc.Client, model.Conversation) connector.RoomOp {
				return connector.RoomOp{Kind: connector.RoomCreate, Name: name, Open: !private}
			}, func(r model.Conversation) string { return "created " + convLabel(r, "") + " " + cell(r.ID) })
		},
	})
	create.Flags().Bool("private", false, "invite-only instead of open to everyone")

	cmd.AddCommand(create,
		roomChange("join <room>", "Join an open room", 1, "",
			func(_ []string, conv model.Conversation) connector.RoomOp {
				return connector.RoomOp{Kind: connector.RoomJoin, Conv: conv.ID}
			},
			func(r model.Conversation, _ string) string { return "joined " + convLabel(r, "") }),
		roomChange("leave <room>", "Leave a room", 1, "",
			func(_ []string, conv model.Conversation) connector.RoomOp {
				return connector.RoomOp{Kind: connector.RoomLeave, Conv: conv.ID}
			},
			func(r model.Conversation, _ string) string { return "left " + convLabel(r, "") }),
		roomChange("invite <room> <handle>", "Invite a person to a room", 2, "handle",
			func(args []string, conv model.Conversation) connector.RoomOp {
				return connector.RoomOp{Kind: connector.RoomInvite, Conv: conv.ID, Handle: model.NormalizeName(args[1])}
			},
			func(r model.Conversation, arg string) string {
				return "invited @" + cell(arg) + " to " + convLabel(r, "")
			}),
		roomChange("remove <room> <handle>", "Remove a person from a room", 2, "handle",
			func(args []string, conv model.Conversation) connector.RoomOp {
				return connector.RoomOp{Kind: connector.RoomRemove, Conv: conv.ID, Handle: model.NormalizeName(args[1])}
			},
			func(r model.Conversation, arg string) string {
				return "removed @" + cell(arg) + " from " + convLabel(r, "")
			}),
		roomChange("rename <room> <name>", "Rename a room", 2, "name",
			func(args []string, conv model.Conversation) connector.RoomOp {
				return connector.RoomOp{Kind: connector.RoomRename, Conv: conv.ID, Name: model.NormalizeName(args[1])}
			},
			func(r model.Conversation, _ string) string { return "renamed to " + convLabel(r, "") }),
	)
	return cmd
}

// roomChange builds a room subcommand that acts on an existing room named by
// its first argument. A second argument, when argKind is set, is validated
// as a "handle" or a room "name" and passed to said as its text.
func roomChange(use, short string, nargs int, argKind string,
	op func(args []string, conv model.Conversation) connector.RoomOp,
	said func(r model.Conversation, arg string) string) *cobra.Command {
	return clientCmd(&cobra.Command{
		Use:   use,
		Short: short,
		Args:  exactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			var err error
			switch argKind {
			case "handle":
				arg, err = handleArg(args[1])
			case "name":
				arg, err = roomArg(args[1])
			}
			if err != nil {
				return err
			}
			return roomOp(cmd, args[0], func(_ *ipc.Client, conv model.Conversation) connector.RoomOp {
				return op(args, conv)
			}, func(r model.Conversation) string { return said(r, arg) })
		},
	})
}

// roomOp resolves ref (when set) to a room, runs the operation and prints the
// resulting room. Fields the server leaves out of its answer are taken from
// the room as it was resolved.
func roomOp(cmd *cobra.Command, ref string, op func(*ipc.Client, model.Conversation) connector.RoomOp,
	said func(r model.Conversation) string) error {
	return withClient(cmd, func(c *ipc.Client) error {
		me, err := whoAmI(c)
		if err != nil {
			return err
		}
		var before model.Conversation
		if ref != "" {
			if before, err = resolveChat(c, ref); err != nil {
				return err
			}
			if before.Kind != model.ConvRoom {
				return exitf(exitNotFound, fmt.Sprintf("not a room: %s", ref))
			}
		}
		rop := op(c, before)
		res, err := c.Room(rop)
		if errors.Is(err, ipc.ErrNotFound) && rop.Handle != "" {
			return exitf(exitNotFound, "no such person: "+rop.Handle)
		}
		if err != nil {
			return err
		}
		if res.ID == "" {
			res.ID = before.ID
		}
		if res.Kind == "" {
			res.Kind = before.Kind
		}
		if res.Name == "" {
			res.Name = before.Name
		}
		return emit(cmd, convOf(res, me), func(w io.Writer) error {
			_, err := fmt.Fprintln(w, said(res))
			return err
		})
	})
}
