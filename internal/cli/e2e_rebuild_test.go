package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/server"
)

// rebuild stops the server and starts another on the same address with a new
// data directory, so it has a new host key and an empty database. It returns
// a store on the new data directory for admin operations.
func (s *testServer) rebuild(t *testing.T) *server.Store {
	t.Helper()
	if err := s.StopServer(); err != nil {
		t.Fatalf("stop server: %v", err)
	}
	s.dir = t.TempDir()
	st, err := server.OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s.start(t)
	return st
}

func texts(ms []msgView) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Text)
	}
	return out
}

// After the server is rebuilt (new host key, new database, the same handles
// and so the same conversation ids), people rejoin as the guide says. New
// messages must arrive and be shown on both sides; the old history and its
// unsent outbox are set aside, not merged and not sent.
func TestRejoinRebuiltServerSetsOldHistoryAside(t *testing.T) {
	s, a, b := pair(t)
	a.ok("dm", "bob")
	for i := range 3 {
		a.send("", "@bob", fmt.Sprintf("old %d", i+1))
	}
	eventually(t, "bob to hold the old messages", func() bool { return len(b.messages("read", "@alice")) == 3 })

	st := s.rebuild(t)
	for _, p := range []*person{a, b} {
		eventually(t, "host_key_changed", func() bool { return stateOf(p) == "host_key_changed" })
	}
	a.send("", "@bob", "queued before the rejoin")
	for _, p := range []*person{a, b} {
		if err := os.Remove(filepath.Join(p.data, "known_servers")); err != nil {
			t.Fatal(err)
		}
	}

	aliceCode, err := server.InitFirstAdmin(st, "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	bobCode, _, err := server.InviteHandle(st, "bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	out := a.tableOK("join", s.addr, "--invite", aliceCode)
	aside := filepath.Join(a.data, "set-aside")
	if !strings.Contains(out, "earlier history set aside in "+aside+string(filepath.Separator)+"alice-") {
		t.Fatalf("join output %q does not say where the history went", out)
	}
	moved, _ := filepath.Glob(filepath.Join(aside, "alice-*", "client.db"))
	if len(moved) != 1 {
		t.Fatalf("set-aside stores %v, want one", moved)
	}
	b.ok("join", s.addr, "--invite", bobCode)
	for _, p := range []*person{a, b} {
		eventually(t, "connected to the rebuilt server", func() bool { return stateOf(p) == "connected" })
	}

	a.ok("dm", "bob")
	a.send("", "@bob", "new 1")
	a.send("", "@bob", "new 2")
	want := []string{"new 1", "new 2"}
	eventually(t, "bob to read the new messages", func() bool { return slices.Equal(texts(b.messages("read", "@alice")), want) })
	eventually(t, "alice to show them as sent", func() bool {
		ms := a.messages("read", "@bob")
		return slices.Equal(texts(ms), want) && ms[0].Status == "" && ms[1].Status == ""
	})
	if got := a.jsonOf("status").(map[string]any)["pending"]; got != float64(0) {
		t.Fatalf("pending = %v, want 0", got)
	}
}
