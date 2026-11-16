package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
)

func TestConfigListGetSet(t *testing.T) {
	_, a, _ := pair(t)
	if out := a.ok("config", "set", "notify.os", "always"); out != "notify.os = always\n" {
		t.Fatalf("set = %q", out)
	}
	if out := a.ok("config", "get", "notify.os"); out != "always\n" {
		t.Fatalf("get = %q", out)
	}
	if out := a.ok("config", "--json"); !strings.Contains(out, `{"Key":"notify.os","Value":"always"`) {
		t.Fatalf("config --json = %s", out)
	}
	b, err := os.ReadFile(filepath.Join(a.cfg, "config.toml"))
	if err != nil || !strings.Contains(string(b), `os = "always"`) {
		t.Fatalf("config.toml = %q, %v", b, err)
	}
	a.ok("config", "set", "notify.os", "false")
}

func TestConfigRoomMode(t *testing.T) {
	_, a, _ := pair(t)
	a.ok("room", "create", "agents")
	if out := a.ok("config", "get", "rooms.agents"); out != "mentions\n" {
		t.Fatalf("default = %q", out)
	}
	if out := a.ok("config", "set", "rooms.agents", "muted"); out != "rooms.agents = muted\n" {
		t.Fatalf("set = %q", out)
	}
	if out := a.ok("config", "get", "rooms.agents"); out != "muted\n" {
		t.Fatalf("get = %q", out)
	}
	a.ok("dm", "bob")
	a.do("config", "set", "rooms.bob", "all").fail(t, 2, "notification modes apply to rooms only")
	a.do("config", "get", "rooms.nowhere").fail(t, 3, "no such chat: nowhere")
}

func TestConfigSetInvalid(t *testing.T) {
	_, a, _ := pair(t)
	a.do("config", "set", "notify.os", "sometimes").fail(t, 2, `notify.os: "sometimes" is not one of false, true, always`)
}

func TestConfigSetRefused(t *testing.T) {
	_, a, _ := pair(t)
	a.do("config", "set", "cli.send", "allow").fail(t, 7, ipc.ErrRefused.Error())
	a.do("config", "set", "notify.hook", "x").fail(t, 7, ipc.ErrRefused.Error())
	a.ok("dm", "bob")
	a.ok("config", "set", "cli.send", "deny")
	a.do("send", "bob", "hi").fail(t, 6, `sending from the command line is disabled (cli.send = "deny")`)
}

func TestRoomModeNeedsMembership(t *testing.T) {
	_, a, b := pair(t)
	a.ok("room", "create", "lounge")
	eventually(t, "the open room to be listed", func() bool {
		for _, c := range b.chats("--all") {
			if c.Name == "lounge" {
				return true
			}
		}
		return false
	})
	b.do("room", "notify", "lounge", "muted").fail(t, 3, "not a member of #lounge")
	b.do("config", "get", "rooms.lounge").fail(t, 3, "not a member of #lounge")
	b.do("config", "set", "rooms.lounge", "muted").fail(t, 3, "not a member of #lounge")
}
