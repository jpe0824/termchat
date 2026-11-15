package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
)

// joinEnv runs the daemon on the configured account at chat.test:7420 with a
// join that records its calls and returns an account on the server it names.
func joinEnv(t *testing.T) (*env, *atomic.Int32) {
	t.Helper()
	e := newEnv(t)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), posted("dana", "m1", "hello"))
	var calls atomic.Int32
	e.adjust = func(o *Options) {
		o.Join = func(_ context.Context, server, _ string) (config.Account, error) {
			calls.Add(1)
			return config.Account{Name: "me", Server: server + ":7420", Handle: "me"}, nil
		}
	}
	e.start()
	waitUntil(t, "connected", func() bool { st, _ := e.c.Status(); return st.Conn == connector.StateConnected })
	return e, &calls
}

func toState(t *testing.T, e *env, s connector.ConnState) {
	t.Helper()
	e.fake.SetState(s)
	waitUntil(t, string(s), func() bool { st, _ := e.c.Status(); return st.Conn == s })
}

// A device whose key was revoked, or whose server's key changed and whose
// user removed the old pin, joins the same server again in place of the
// account it had.
func TestJoinReplacesAccountStuckInTerminalState(t *testing.T) {
	for _, s := range []connector.ConnState{connector.StateNotAuthorised, connector.StateHostKeyChanged} {
		t.Run(string(s), func(t *testing.T) {
			e, calls := joinEnv(t)
			toState(t, e, s)
			if err := e.c.Join("chat.test", "again"); err != nil {
				t.Fatalf("Join: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("join called %d times", calls.Load())
			}
			saved, err := config.Load()
			if err != nil || len(saved.Accounts) != 1 || saved.Accounts[0].Server != "chat.test:7420" {
				t.Fatalf("saved config = %+v, %v", saved, err)
			}
			e.fake.SetState(connector.StateConnected)
			waitUntil(t, "the replaced account connected", func() bool {
				st, _ := e.c.Status()
				return st.Conn == connector.StateConnected && st.Account == "me"
			})
			if ids := chatIDs(t, e.c); len(ids) != 1 || ids[0] != "r1" {
				t.Fatalf("chats = %v", ids)
			}
		})
	}
}

func TestJoinStillRefusedOtherwise(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  connector.ConnState
		server string
	}{
		{"connected", connector.StateConnected, "chat.test"},
		{"offline", connector.StateOffline, "chat.test"},
		{"version mismatch", connector.StateVersionMismatch, "chat.test"},
		{"another server", connector.StateNotAuthorised, "other.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, calls := joinEnv(t)
			if tc.state != connector.StateConnected {
				toState(t, e, tc.state)
			}
			err := e.c.Join(tc.server, "again")
			if !errors.Is(err, ipc.ErrInvalid) || !strings.Contains(err.Error(), "an account is already configured") {
				t.Fatalf("Join = %v, want ErrInvalid: an account is already configured", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("join was called %d times", calls.Load())
			}
		})
	}
}

// keyedConn is a connector that pins the server's key.
type keyedConn struct{ connector.Connector }

func (keyedConn) ServerKey() string { return "SHA256:pinned" }

func TestStatusShowsThePinnedServerKey(t *testing.T) {
	e := newEnv(t)
	e.conn = keyedConn{e.fake}
	c := e.start()
	waitUntil(t, "connected", func() bool { st, _ := c.Status(); return st.Conn == connector.StateConnected })
	if st, err := c.Status(); err != nil || st.ServerKey != "SHA256:pinned" {
		t.Fatalf("Status = %+v, %v; want ServerKey SHA256:pinned", st, err)
	}
}
