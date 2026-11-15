package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/server"
)

// rebuilt is a pair whose server was rebuilt after three old DMs: both
// clients reached host_key_changed, alice queued one more message, and both
// pins were deleted as the guide says. The codes are fresh invites on the new
// server.
type rebuilt struct {
	s                  *testServer
	a, b               *person
	aliceCode, bobCode string
}

func rebuildPair(t *testing.T) rebuilt {
	t.Helper()
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
	r := rebuilt{s: s, a: a, b: b}
	var err error
	if r.aliceCode, err = server.InitFirstAdmin(st, "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if r.bobCode, _, err = server.InviteHandle(st, "bob", "Bob"); err != nil {
		t.Fatal(err)
	}
	return r
}

// flows checks that a new exchange shows on both sides, and nothing else:
// no old history and nothing queued against the old server.
func (r rebuilt) flows(t *testing.T) {
	t.Helper()
	for _, p := range []*person{r.a, r.b} {
		eventually(t, "connected to the rebuilt server", func() bool { return stateOf(p) == "connected" })
	}
	r.a.ok("dm", "bob")
	r.a.send("", "@bob", "new 1")
	r.b.send("", "@alice", "new 2")
	want := []string{"new 1", "new 2"}
	for _, c := range []struct {
		p    *person
		with string
	}{{r.a, "@bob"}, {r.b, "@alice"}} {
		eventually(t, "both new messages on both sides", func() bool {
			ms := c.p.messages("read", c.with)
			return slices.Equal(texts(ms), want) && ms[0].Status == "" && ms[1].Status == ""
		})
	}
	if got := r.a.jsonOf("status").(map[string]any)["pending"]; got != float64(0) {
		t.Fatalf("pending = %v, want 0", got)
	}
}

// textsOnceListed is the chat's message texts, or nil while the chat is not
// listed yet, as on a fresh store before its first catch-up ends.
func textsOnceListed(t *testing.T, p *person, chat string) []string {
	t.Helper()
	r := p.do("read", chat, "--json")
	if r.code != 0 {
		return nil
	}
	var ms []msgView
	decode(t, r.out, &ms)
	return texts(ms)
}

func setAsideStores(t *testing.T, p *person) []string {
	t.Helper()
	got, _ := filepath.Glob(filepath.Join(p.data, "set-aside", "*", "client.db"))
	return got
}

// The pins were deleted and both background processes restarted before the
// rejoin, so the accounts came back up as not_authorised. The stores still
// belong to the old server and are set aside.
func TestRejoinAfterRestartSetsOldHistoryAside(t *testing.T) {
	r := rebuildPair(t)
	for _, p := range []*person{r.a, r.b} {
		p.ok("daemon", "stop")
		eventually(t, "not_authorised after the restart", func() bool { return stateOf(p) == "not_authorised" })
	}
	out := r.a.tableOK("join", r.s.addr, "--invite", r.aliceCode)
	if !strings.Contains(out, "earlier history set aside in "+filepath.Join(r.a.data, "set-aside")+string(filepath.Separator)+"alice-") {
		t.Fatalf("join output %q does not say where the history went", out)
	}
	r.b.ok("join", r.s.addr, "--invite", r.bobCode)
	for _, p := range []*person{r.a, r.b} {
		if n := len(setAsideStores(t, p)); n != 1 {
			t.Fatalf("%d set-aside stores, want 1", n)
		}
	}
	r.flows(t)
}

// rebuiltWithObstacle has alice's join succeed at the server but fail to set
// her history aside, because set-aside is a file. It returns the reason.
func rebuiltWithObstacle(t *testing.T) (rebuilt, string) {
	t.Helper()
	r := rebuildPair(t)
	obstacle := filepath.Join(r.a.data, "set-aside")
	if err := os.WriteFile(obstacle, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reason := "set aside the earlier history: mkdir " + obstacle + ": not a directory"
	r.a.do("join", r.s.addr, "--invite", r.aliceCode).fail(t, 1, reason)

	var st struct {
		Account, State, Problem string
	}
	if err := json.Unmarshal([]byte(r.a.ok("status", "--json")), &st); err != nil {
		t.Fatal(err)
	}
	if st.Account != "alice" || st.State != "offline" || st.Problem != reason {
		t.Fatalf("status after the failed join = %+v", st)
	}
	r.a.tableOK("status") // the table shows it too, without failing

	// Bob is on the new server and writes; alice's old store must not be
	// opened against it, so she shows nothing rather than a mix.
	r.b.ok("join", r.s.addr, "--invite", r.bobCode)
	eventually(t, "bob connected", func() bool { return stateOf(r.b) == "connected" })
	r.b.ok("dm", "alice")
	r.b.send("", "@alice", "while alice is stuck")
	r.a.do("read", "@bob").fail(t, 1, "account unavailable: "+reason)

	if err := os.Remove(obstacle); err != nil {
		t.Fatal(err)
	}
	return r, reason
}

func TestFailedSetAsideRecoversOnRetriedJoin(t *testing.T) {
	r, _ := rebuiltWithObstacle(t)
	out := r.a.tableOK("join", r.s.addr, "--invite", r.aliceCode)
	if !strings.Contains(out, "earlier history set aside in ") {
		t.Fatalf("retried join output %q", out)
	}
	if n := len(setAsideStores(t, r.a)); n != 1 {
		t.Fatalf("%d set-aside stores, want 1", n)
	}
	eventually(t, "bob's message while alice was stuck", func() bool {
		return slices.Equal(textsOnceListed(t, r.a, "@bob"), []string{"while alice is stuck"})
	})
}

func TestFailedSetAsideRecoversOnRestart(t *testing.T) {
	r, _ := rebuiltWithObstacle(t)
	r.a.ok("daemon", "stop")
	eventually(t, "alice connected after the restart", func() bool { return stateOf(r.a) == "connected" })
	if n := len(setAsideStores(t, r.a)); n != 1 {
		t.Fatalf("%d set-aside stores, want 1", n)
	}
	var st struct {
		SetAside string `json:"set_aside"`
	}
	if err := json.Unmarshal([]byte(r.a.ok("status", "--json")), &st); err != nil || !strings.HasPrefix(st.SetAside, filepath.Join(r.a.data, "set-aside", "alice-")) {
		t.Fatalf("status set_aside = %q, %v", st.SetAside, err)
	}
	eventually(t, "bob's message while alice was stuck", func() bool {
		return slices.Equal(textsOnceListed(t, r.a, "@bob"), []string{"while alice is stuck"})
	})
}
