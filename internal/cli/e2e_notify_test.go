package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
)

// quietConfig is every simulated person's config.toml: test traffic must
// never reach the desktop through an OS notification or a hook. It has to exist
// before the person's first command.
const quietConfig = `[notify]
os = false
hook = ""
`

// checkQuietConfig reports why the config.toml at path would let a
// background process post an OS notification or run a hook. A missing os
// key counts, since it defaults to true. os is decoded loosely because the
// setting is not limited to a boolean; only false is quiet.
func checkQuietConfig(path string) error {
	var c struct {
		Notify struct {
			OS   any    `toml:"os"`
			Hook string `toml:"hook"`
		} `toml:"notify"`
	}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return err
	}
	if c.Notify.OS == nil {
		return errors.New("notify.os is missing, so the OS route is on")
	}
	if c.Notify.OS != false {
		return fmt.Errorf("notify.os = %#v, want false", c.Notify.OS)
	}
	if c.Notify.Hook != "" {
		return fmt.Errorf("notify.hook = %q, want none", c.Notify.Hook)
	}
	return nil
}

func TestCheckQuietConfig(t *testing.T) {
	account := "\n[[account]]\nname = \"a\"\nserver = \"127.0.0.1:7420\"\nhandle = \"alice\"\n"
	cases := []struct {
		name, file string
		write      bool
		want       string // substring of the error; "" means nil
		notExist   bool
	}{
		{name: "quiet", file: quietConfig, write: true},
		{name: "quiet after join", file: quietConfig + account, write: true},
		{name: "no file", notExist: true},
		{name: "no notify section", file: "[cli]\nsend = \"allow\"\n", write: true, want: "notify.os is missing"},
		{name: "os missing", file: "[notify]\nhook = \"\"\n", write: true, want: "notify.os is missing"},
		{name: "os true", file: "[notify]\nos = true\n", write: true, want: "notify.os = true"},
		{name: "os always", file: "[notify]\nos = \"always\"\n", write: true, want: `notify.os = "always"`},
		{name: "hook set", file: "[notify]\nos = false\nhook = \"/bin/echo\"\n", write: true, want: `notify.hook = "/bin/echo"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if c.write {
				if err := os.WriteFile(path, []byte(c.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := checkQuietConfig(path)
			switch {
			case c.notExist:
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("err = %v, want fs.ErrNotExist", err)
				}
			case c.want == "":
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want it to contain %q", err, c.want)
				}
			}
		})
	}
}

func TestJoinKeepsNotifySection(t *testing.T) {
	s := startServer(t)
	a := newPerson(t)
	s.joinAs(t, a, "alice", "Alice")
	path := filepath.Join(a.cfg, "config.toml")
	if err := checkQuietConfig(path); err != nil {
		t.Fatalf("after join: %v", err)
	}
	var c config.Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Accounts) != 1 || c.Accounts[0].Handle != "alice" {
		t.Fatalf("accounts after join = %+v, want only alice", c.Accounts)
	}
}

// hookCounter installs a hook on p's running background process that appends
// a line to the returned file for each notification, and removes it when the
// test ends so the quiet-config check holds. The CLI refuses to set a hook,
// so the test sets it as the interface would.
func hookCounter(t *testing.T, p *person) string {
	t.Helper()
	out := filepath.Join(p.dir, "hook.out")
	script := filepath.Join(p.dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho run >> "+out+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	p.ipcDo(func(c *ipc.Client) error { _, err := c.ConfigSet("notify.hook", script, "tui"); return err })
	t.Cleanup(func() {
		p.ipcDo(func(c *ipc.Client) error { _, err := c.ConfigSet("notify.hook", "", "tui"); return err })
	})
	return out
}

func hookRuns(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "run\n")
}

func TestRoomNotifyAppliesWithoutRestart(t *testing.T) {
	_, a, b := pair(t)
	a.ok("room", "create", "agents")
	b.ok("room", "join", "agents")
	runs := hookCounter(t, b)

	a.ok("send", "agents", "first, no mention")
	eventually(t, "bob to hold the first message", func() bool { return len(b.messages("read", "agents")) == 1 })
	if n := hookRuns(runs); n != 0 {
		t.Fatalf("hook ran %d times in a mentions-only room", n)
	}
	if out := b.ok("room", "notify", "agents", "all"); out != "#agents notifies: all\n" {
		t.Fatalf("notify = %q", out)
	}
	a.ok("send", "agents", "second")
	eventually(t, "the hook to run", func() bool { return hookRuns(runs) == 1 })
}

func TestRoomNotifyRefusesDM(t *testing.T) {
	_, a, _ := pair(t)
	a.ok("dm", "bob")
	a.do("room", "notify", "bob", "all").failContains(t, 2, "rooms only")
}
