package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
	"github.com/jpe0824/termchat/internal/server"
)

const passphraseEnv = "TERMCHAT_BACKUP_PASSPHRASE"

// Replaced by tests, which have no terminal.
var (
	stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	readPassword    = func() (string, error) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		return string(b), err
	}
)

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Administer a chat server's data directory",
		Args:  noSubcommand,
		RunE:  groupRun,
	}
	addDataFlag(cmd, true)

	user := &cobra.Command{Use: "user", Short: "Manage people", Args: noSubcommand, RunE: groupRun}
	user.AddCommand(
		&cobra.Command{Use: "list", Short: "List people", Args: exactArgs(0), RunE: runUserList},
		&cobra.Command{Use: "remove <handle>", Short: "Remove a person and revoke their keys", Args: exactArgs(1), RunE: runUserRemove},
		adminCmd(),
	)
	key := &cobra.Command{Use: "key", Short: "Manage device keys", Args: noSubcommand, RunE: groupRun}
	key.AddCommand(
		&cobra.Command{Use: "list <handle>", Short: "List a person's keys", Args: exactArgs(1), RunE: runKeyList},
		&cobra.Command{Use: "remove <fingerprint>", Short: "Revoke one key", Args: exactArgs(1), RunE: runKeyRemove},
	)
	room := &cobra.Command{Use: "room", Short: "Inspect rooms", Args: noSubcommand, RunE: groupRun}
	room.AddCommand(&cobra.Command{Use: "list", Short: "List rooms", Args: exactArgs(0), RunE: runRoomList})

	invite := &cobra.Command{Use: "invite <handle>", Short: "Create an invite code", Args: exactArgs(1), RunE: runInvite}
	invite.Flags().String("name", "", "display name for a new person")
	initCmd := &cobra.Command{Use: "init", Short: "Create the first admin", Args: exactArgs(0), RunE: runInit}
	initCmd.Flags().String("handle", "", "handle of the first admin")
	initCmd.Flags().String("name", "", "display name of the first admin")

	cmd.AddCommand(
		initCmd, invite, user, key, room,
		&cobra.Command{Use: "backup <file>", Short: "Write an encrypted backup", Args: exactArgs(1), RunE: runBackup},
		&cobra.Command{Use: "restore <file>", Short: "Restore a backup into an empty --data directory", Args: exactArgs(1), RunE: runRestore},
	)
	return cmd
}

func adminCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "admin <handle>", Short: "Grant or revoke admin rights", Args: exactArgs(1), RunE: runUserAdmin}
	cmd.Flags().Bool("revoke", false, "revoke instead of grant")
	return cmd
}

// openStore opens an existing server database. Only init may create one.
func openStore(cmd *cobra.Command, create bool) (*server.Store, error) {
	dir, err := dataDir(cmd)
	if err != nil {
		return nil, err
	}
	if !create {
		if _, err := os.Stat(server.DBPath(dir)); err != nil {
			return nil, fmt.Errorf("no server database in %s (run \"termchat server init\" first)", dir)
		}
	}
	return server.OpenStore(dir)
}

// useStore runs fn with the server database open.
func useStore(cmd *cobra.Command, create bool, fn func(*server.Store) error) error {
	st, err := openStore(cmd, create)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

// friendly turns store errors into messages that name what to do.
func friendly(err error, handle string) error {
	switch {
	case errors.Is(err, server.ErrInvalidName):
		return usagef("invalid handle %q: use lowercase letters, digits, - and _, starting with a letter or digit, at most 32 characters", handle)
	case errors.Is(err, server.ErrNotEmpty):
		return errors.New("this server already has people; init only works on an empty server")
	case errors.Is(err, server.ErrLastAdmin):
		return errors.New("refusing: that is the last admin; make another person an admin first")
	}
	return err
}

func printJoinHint(out io.Writer, code string) {
	fmt.Fprintf(out, "invite code: %s (valid for %s)\n", code, server.InviteTTL)
	fmt.Fprintln(out, "on the client, run (replace <host:port> with the address clients reach this server at; the default port is 7420):")
	fmt.Fprintf(out, "  termchat join <host:port> --invite %s\n", code)
}

func runInit(cmd *cobra.Command, _ []string) error {
	handle, _ := cmd.Flags().GetString("handle")
	name, _ := cmd.Flags().GetString("name")
	if handle == "" {
		return usagef("--handle is required")
	}
	return useStore(cmd, true, func(st *server.Store) error {
		code, err := server.InitFirstAdmin(st, handle, name)
		if err != nil {
			return friendly(err, handle)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "created admin %s\n", shown(handle))
		printJoinHint(out, code)
		return nil
	})
}

func runInvite(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	return useStore(cmd, false, func(st *server.Store) error {
		code, existing, err := server.InviteHandle(st, args[0], name)
		if errors.Is(err, server.ErrExists) {
			return fmt.Errorf("handle %s was removed and cannot be invited again", sanitize.Text(args[0]))
		}
		if err != nil {
			return friendly(err, args[0])
		}
		out := cmd.OutOrStdout()
		h := shown(args[0])
		if existing {
			fmt.Fprintf(out, "invite adds another device to %s\n", h)
		} else {
			fmt.Fprintf(out, "invite creates the new person %s\n", h)
		}
		printJoinHint(out, code)
		return nil
	})
}

func runUserList(cmd *cobra.Command, _ []string) error {
	return useStore(cmd, false, func(st *server.Store) error {
		people, err := st.People()
		if err != nil {
			return err
		}
		tw := table(cmd.OutOrStdout())
		for _, p := range people {
			var flags []string
			if p.Admin {
				flags = append(flags, "admin")
			}
			if p.Agent {
				flags = append(flags, "agent (owner "+cell(p.Owner)+")")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", cell(p.Handle), cell(p.DisplayName), cell(strings.Join(flags, ", ")))
		}
		return tw.Flush()
	})
}

func runUserRemove(cmd *cobra.Command, args []string) error {
	return useStore(cmd, false, func(st *server.Store) error {
		res, err := server.RemovePersonFully(st, model.NormalizeName(args[0]), args[0], nil)
		if err != nil {
			return friendly(err, args[0])
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s: left %d room(s), revoked %d key(s)\n",
			shown(args[0]), len(res.Events), len(res.Keys))
		return nil
	})
}

func runUserAdmin(cmd *cobra.Command, args []string) error {
	revoke, _ := cmd.Flags().GetBool("revoke")
	return useStore(cmd, false, func(st *server.Store) error {
		if err := server.SetAdminRights(st, args[0], !revoke); err != nil {
			if errors.Is(err, server.ErrAgent) {
				return fmt.Errorf("%s is an agent account; agents cannot be admins", sanitize.Text(args[0]))
			}
			return friendly(err, args[0])
		}
		verb := "granted admin to"
		if revoke {
			verb = "revoked admin from"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, shown(args[0]))
		return nil
	})
}

func runKeyList(cmd *cobra.Command, args []string) error {
	return useStore(cmd, false, func(st *server.Store) error {
		keys, err := st.Keys(args[0])
		if err != nil {
			return friendly(err, args[0])
		}
		tw := table(cmd.OutOrStdout())
		for _, k := range keys {
			fmt.Fprintf(tw, "%s\t%s\n", cell(k.Fingerprint), k.AddedAt.UTC().Format(time.RFC3339))
		}
		return tw.Flush()
	})
}

func runKeyRemove(cmd *cobra.Command, args []string) error {
	return useStore(cmd, false, func(st *server.Store) error {
		if err := st.RemoveKey(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed key %s\n", sanitize.Text(args[0]))
		return nil
	})
}

func runRoomList(cmd *cobra.Command, _ []string) error {
	return useStore(cmd, false, func(st *server.Store) error {
		rooms, err := server.ListRooms(st)
		if err != nil {
			return err
		}
		tw := table(cmd.OutOrStdout())
		for _, r := range rooms {
			kind := "private"
			if r.Open {
				kind = "open"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\n", cell(r.Name), kind, r.Members)
		}
		return tw.Flush()
	})
}

// passphrase reads the backup passphrase from the environment or, on a
// terminal, from a prompt that does not echo. confirm asks twice.
func passphrase(cmd *cobra.Command, confirm bool) (string, error) {
	if p := os.Getenv(passphraseEnv); p != "" {
		return p, nil
	}
	if !stdinIsTerminal() {
		return "", fmt.Errorf("no passphrase: set %s, or run on a terminal to be prompted", passphraseEnv)
	}
	errOut := cmd.ErrOrStderr()
	ask := func(prompt string) (string, error) {
		fmt.Fprint(errOut, prompt)
		p, err := readPassword()
		fmt.Fprintln(errOut)
		return p, err
	}
	p, err := ask("passphrase: ")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", server.ErrEmptyPassphrase
	}
	if confirm {
		again, err := ask("repeat passphrase: ")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errors.New("passphrases do not match")
		}
	}
	return p, nil
}

func runBackup(cmd *cobra.Command, args []string) error {
	dir, err := dataDir(cmd)
	if err != nil {
		return err
	}
	pass, err := passphrase(cmd, true)
	if err != nil {
		return err
	}
	if err := server.Backup(dir, args[0], pass); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "backup written to %s\n", args[0])
	return nil
}

func runRestore(cmd *cobra.Command, args []string) error {
	dir, err := dataDir(cmd)
	if err != nil {
		return err
	}
	pass, err := passphrase(cmd, false)
	if err != nil {
		return err
	}
	if err := server.Restore(args[0], dir, pass); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "restored into %s\n", dir)
	return nil
}
