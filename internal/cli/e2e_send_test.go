package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/store"
)

type sent struct {
	MsgID string `json:"msg_id"`
	State string `json:"state"`
}

func (p *person) send(stdin string, args ...string) sent {
	p.t.Helper()
	r := p.exec(stdin, append([]string{"send", "--json"}, args...)...)
	if r.code != 0 || r.err != "" {
		p.t.Fatalf("send %v: exit %d, stderr %q", args, r.code, r.err)
	}
	var s sent
	decode(p.t, r.out, &s)
	return s
}

func TestSendFromArgumentAndStdin(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")

	if s := a.send("", "@bob", "from the argument"); s.State != "sending" || s.MsgID == "" {
		t.Fatalf("send = %+v", s)
	}
	a.send("first line\nsecond line\n", "@bob")
	a.send("dash form\n", "@bob", "-")
	a.send("keeps one blank\n\n", "@bob")
	a.send("windows line\r\n", "@bob")
	eventually(t, "all five", func() bool { return len(b.messages("read", "@alice")) == 5 })
	var got []string
	for _, m := range b.messages("read", "@alice") {
		got = append(got, m.Text)
	}
	want := []string{"from the argument", "first line\nsecond line", "dash form", "keeps one blank\n", "windows line"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %q, want %q", got, want)
	}
	table := a.tableOK("send", "@bob", "table mode")
	if !strings.HasPrefix(table, "queued ") || !strings.HasSuffix(table, " (sending)\n") {
		t.Fatalf("send table = %q", table)
	}
}

func TestSendRejectsEmptyAndOversizeText(t *testing.T) {
	_, a, _ := pair(t)
	a.ok("dm", "bob")
	const msg = "no message text; give it as an argument or on stdin"
	a.do("send", "@bob", "").fail(t, 2, msg)
	a.exec("\n", "send", "@bob").fail(t, 2, msg)
	a.exec("", "send", "@bob", "-").fail(t, 2, msg)
	a.exec("  \n", "send", "@bob").fail(t, 2, msg)
	a.do("send", "@bob", "a", "b").fail(t, 2, "termchat send takes at most 2 argument(s), got 3")
	a.do("send", "@bob", strings.Repeat("x", 8193)).fail(t, 2, "the message is 8193 bytes; the limit is 8192")
	if s := a.send("", "@bob", strings.Repeat("x", 8192)); s.State != "sending" {
		t.Fatalf("a message of exactly the limit: %+v", s)
	}
}

func TestSendDeniedExits6(t *testing.T) {
	_, a, _ := pair(t)
	a.ok("dm", "bob")
	a.ok("daemon", "stop")
	cfg := filepath.Join(a.cfg, "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(data), `send = "allow"`) {
		t.Fatalf("config = %q (%v)", data, err)
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(data), `send = "allow"`, `send = "deny"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	a.do("send", "@bob", "nope").fail(t, 6, `sending from the command line is disabled (cli.send = "deny")`)
	if ms := a.messages("read", "@bob"); len(ms) != 0 {
		t.Fatalf("a denied send left %+v", ms)
	}
}

func TestSendReportsFailureNotSilence(t *testing.T) {
	s, a, b := pair(t)
	a.ok("dm", "bob")
	if err := s.StopServer(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to notice", func() bool {
		m, _ := a.jsonOf("status").(map[string]any)
		return m["state"] != "connected"
	})
	queued := a.send("", "@bob", "sent while the server is down")
	if queued.State != "sending" {
		t.Fatalf("send while offline = %+v, want state sending", queued)
	}
	if m := a.jsonOf("status").(map[string]any); m["pending"] != float64(1) {
		t.Fatalf("status = %v, want 1 pending", m)
	}
	if out := a.tableOK("status"); !strings.Contains(out, "pending  1\n") {
		t.Fatalf("status table = %q", out)
	}
	own := a.messages("read", "@bob")
	if len(own) != 1 || own[0].Status != "sending" || own[0].MsgID != queued.MsgID || own[0].Seq != 0 || own[0].Author != "alice" {
		t.Fatalf("own unsent item = %+v", own)
	}
	if out := a.tableOK("read", "@bob"); !strings.Contains(out, "(sending)") || !strings.Contains(out, "sent while the server is down") {
		t.Fatalf("read table = %q", out)
	}

	s.start(t)
	eventually(t, "delivery", func() bool { return len(b.messages("read", "@alice")) == 1 })
	eventually(t, "outbox to drain", func() bool { return a.jsonOf("status").(map[string]any)["pending"] == float64(0) })
	got := b.messages("read", "@alice")
	if len(got) != 1 || got[0].Text != "sent while the server is down" || got[0].MsgID != queued.MsgID {
		t.Fatalf("bob reads %+v, want the one message exactly once", got)
	}
	if own = a.messages("read", "@bob"); len(own) != 1 || own[0].Status != "" || own[0].Seq == 0 {
		t.Fatalf("alice reads %+v after delivery, want one stored message", own)
	}
}

func TestReadShowsFailedItemWithReason(t *testing.T) {
	s, a, _ := pair(t)
	a.ok("dm", "bob")
	if err := s.StopServer(); err != nil {
		t.Fatal(err)
	}
	queued := a.send("", "@bob", "this one fails")
	a.ok("daemon", "stop")
	eventually(t, "daemon to exit", func() bool { return len(a.daemons()) == 0 })
	st, err := store.Open(a.data, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFailed(queued.MsgID, "the server refused: too noisy"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ms := a.messages("read", "@bob")
	if len(ms) != 1 || ms[0].Status != "failed" || ms[0].Error != "the server refused: too noisy" {
		t.Fatalf("read = %+v, want failed with its reason", ms)
	}
	if out := a.tableOK("read", "@bob"); !strings.Contains(out, "(failed: the server refused: too noisy)") {
		t.Fatalf("table = %q", out)
	}
}

func TestSendTextMayStartWithDashes(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	for _, text := range []string{"- done", "-1", "--json", "--version now", "-n"} {
		if s := a.send("", append([]string{"@bob"}, text)...); s.State != "sending" || s.MsgID == "" {
			t.Fatalf("send %q = %+v", text, s)
		}
	}
	// Flags go before the chat; "--" still works.
	if r := a.do("send", "--json", "@bob", "flag first"); r.code != 0 || !strings.HasPrefix(r.out, `{"msg_id":`) {
		t.Fatalf("send --json @bob: exit %d, %q, %q", r.code, r.out, r.err)
	}
	a.ok("send", "--", "@bob", "--after double dash")
	a.exec("from stdin\n", "send", "@bob", "-")
	eventually(t, "all messages", func() bool { return len(b.messages("read", "@alice")) == 8 })
	var got []string
	for _, m := range b.messages("read", "@alice") {
		got = append(got, m.Text)
	}
	want := []string{"- done", "-1", "--json", "--version now", "-n", "flag first", "--after double dash", "from stdin"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %q, want %q", got, want)
	}
}
