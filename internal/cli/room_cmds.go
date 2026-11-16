package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

func newRoomCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "room", Short: "Create and manage rooms", Args: noSubcommand, RunE: groupRun}

	create := clientCmd(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a room (open to everyone unless --private)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := nameArg("room name", args[0])
			if err != nil {
				return err
			}
			private, _ := cmd.Flags().GetBool("private")
			return roomOp(cmd, "", func(model.Conversation) ipc.RoomOp {
				return ipc.RoomOp{Kind: ipc.RoomCreate, Name: name, Open: !private}
			}, func(r model.Conversation) string { return "created " + convLabel(r, "") + " " + cell(r.ID) })
		},
	})
	create.Flags().Bool("private", false, "invite-only instead of open to everyone")

	notify := clientCmd(&cobra.Command{
		Use:   "notify <room> all|mentions|muted",
		Short: "Set how a room notifies you; it applies at once",
		Args:  exactArgs(2),
		RunE:  roomNotify,
	})

	cmd.AddCommand(create, notify)
	for _, c := range roomChanges {
		cmd.AddCommand(roomChange(c))
	}
	return cmd
}

// roomChangeSpec describes a room subcommand that acts on an existing room
// named by its first argument. A second argument, when arg is set, is
// validated as a "handle" or a "room name", and goes into the operation's
// Handle or Name.
type roomChangeSpec struct {
	use, short string
	kind       string
	arg        string
	// said is what the command prints; arg is the validated second argument.
	said func(r model.Conversation, arg string) string
}

var roomChanges = []roomChangeSpec{
	{"join <room>", "Join an open room", ipc.RoomJoin, "",
		func(r model.Conversation, _ string) string { return "joined " + convLabel(r, "") }},
	{"leave <room>", "Leave a room", ipc.RoomLeave, "",
		func(r model.Conversation, _ string) string { return "left " + convLabel(r, "") }},
	{"invite <room> <handle>", "Invite a person to a room", ipc.RoomInvite, "handle",
		func(r model.Conversation, arg string) string {
			return "invited @" + cell(arg) + " to " + convLabel(r, "")
		}},
	{"remove <room> <handle>", "Remove a person from a room", ipc.RoomRemove, "handle",
		func(r model.Conversation, arg string) string {
			return "removed @" + cell(arg) + " from " + convLabel(r, "")
		}},
	{"rename <room> <name>", "Rename a room", ipc.RoomRename, "room name",
		func(r model.Conversation, _ string) string { return "renamed to " + convLabel(r, "") }},
}

func roomChange(spec roomChangeSpec) *cobra.Command {
	nargs := 1
	if spec.arg != "" {
		nargs = 2
	}
	return clientCmd(&cobra.Command{
		Use:   spec.use,
		Short: spec.short,
		Args:  exactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if spec.arg != "" {
				var err error
				if arg, err = nameArg(spec.arg, args[1]); err != nil {
					return err
				}
			}
			return roomOp(cmd, args[0], func(conv model.Conversation) ipc.RoomOp {
				op := ipc.RoomOp{Kind: spec.kind, Conv: conv.ID}
				if spec.arg == "handle" {
					op.Handle = arg
				} else {
					op.Name = arg
				}
				return op
			}, func(r model.Conversation) string { return spec.said(r, arg) })
		},
	})
}

// roomOp resolves ref (when set) to a room, runs the operation and prints the
// resulting room. Fields the server leaves out of its answer are taken from
// the room as it was resolved.
func roomOp(cmd *cobra.Command, ref string, op func(model.Conversation) ipc.RoomOp,
	said func(r model.Conversation) string) error {
	return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
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
		rop := op(before)
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

func roomNotify(cmd *cobra.Command, args []string) error {
	return withClient(cmd, cmdContext(cmd), func(c *ipc.Client) error {
		room, err := memberRoom(c, args[0])
		if err != nil {
			return err
		}
		key := config.RoomPrefix + room.ID
		if err := config.Validate(key, args[1]); err != nil {
			return usagef("%s", sanitize.Line(err.Error()))
		}
		s, err := c.ConfigSet(key, args[1], configSource)
		if err != nil {
			return err
		}
		return printSetting(cmd, s, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "#%s notifies: %s\n", cell(room.Name), cell(s.Value))
			return err
		})
	})
}
