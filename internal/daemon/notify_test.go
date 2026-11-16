package daemon

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// noteRec is a notifier that records what it is asked to raise.
type noteRec struct {
	mu    sync.Mutex
	os    []ipc.NotifyPayload
	hooks []ipc.NotifyPayload
	cmds  []string // the hook command each Hook call was given
	sent  int      // sentinels pushed, touched only by the test goroutine
}

func (r *noteRec) OS(p ipc.NotifyPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.os = append(r.os, p)
}

func (r *noteRec) Hook(p ipc.NotifyPayload, hook string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, p)
	r.cmds = append(r.cmds, hook)
}

func (r *noteRec) hookCmds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.cmds)
}

func (r *noteRec) osCalls() []ipc.NotifyPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.os)
}

func (r *noteRec) hookCalls() []ipc.NotifyPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.hooks)
}

func dm(id, other string) model.Conversation {
	return model.Conversation{ID: id, Kind: model.ConvDM, Members: []string{"me", other}}
}

// noteEnv prepares an env with a recording notifier and dana known by name.
func noteEnv(t *testing.T) (*env, *noteRec) {
	t.Helper()
	e := newEnv(t)
	rec := &noteRec{}
	e.adjust = func(o *Options) { o.Notifier = rec }
	e.fake.AddPeople(model.Person{Handle: "dana", DisplayName: "Dana"}, model.Person{Handle: "sam"})
	e.cfg.Rooms = map[string]string{}
	return e, rec
}

// sentinel pushes a message that always notifies and waits for it. The drain
// goroutine handles pushes in order, so everything pushed before it has been
// decided by the time it shows.
func sentinel(t *testing.T, e *env, rec *noteRec) {
	t.Helper()
	rec.sent++
	text := fmt.Sprintf("sentinel %d", rec.sent)
	push(e, "sd", "sam", fmt.Sprintf("s%d", rec.sent), text, 0)
	waitUntil(t, "sentinel notification", func() bool { return slices.Contains(texts(rec.osCalls()), text) })
}

// started adds convs, which hold no messages, and runs the daemon.
func started(t *testing.T, e *env, convs ...model.Conversation) *ipc.Client {
	t.Helper()
	for _, c := range convs {
		e.fake.AddConversation(c)
	}
	return startedWith(t, e, len(convs))
}

// startedWith runs the daemon once its n conversations are in the fake.
func startedWith(t *testing.T, e *env, n int) *ipc.Client {
	t.Helper()
	e.fake.AddConversation(dm("sd", "sam"))
	c := e.start()
	waitUntil(t, "conversations", func() bool { return len(chatIDs(t, c)) == n+1 })
	waitUntil(t, "online", func() bool { st, _ := c.Status(); return st.Conn == connector.StateConnected && !st.Syncing })
	return c
}

func texts(ps []ipc.NotifyPayload) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Text)
	}
	return out
}

func dropSentinels(ps []ipc.NotifyPayload) []ipc.NotifyPayload {
	return slices.DeleteFunc(slices.Clone(ps), func(p ipc.NotifyPayload) bool { return p.ConvID == "sd" })
}

// push delivers a live message event; seq 0 takes the next number.
func push(e *env, conv, author, msgID, text string, seq int64, mentions ...string) model.Event {
	ev := model.Event{ConvID: conv, Seq: seq, Kind: model.MessagePosted, Actor: author, MsgID: msgID,
		Mentions: mentions, Body: &model.Body{Format: "text/plain", Data: []byte(text)}}
	e.fake.Push(ev)
	return ev
}

func TestLiveDMNotifiesWithPayload(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	push(e, "d1", "dana", "m1", "hello there", 0, "me")
	sentinel(t, e, rec)
	want := []ipc.NotifyPayload{
		{ConvID: "d1", ConvName: "dana", Sender: "Dana", Text: "hello there", Mention: true, DM: true},
		{ConvID: "sd", ConvName: "sam", Sender: "sam", Text: "sentinel 1", DM: true},
	}
	if got := rec.osCalls(); !slices.Equal(got, want) {
		t.Fatalf("OS notifications = %+v\nwant %+v", got, want)
	}
	if h := rec.hookCalls(); len(h) != 0 {
		t.Errorf("hook ran with none configured: %+v", h)
	}
}

func TestRoomPayloadAndTextCleaning(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Rooms["r1"] = "all"
	started(t, e, room("r1", "platform", "me", "dana"))
	long := strings.Repeat("é", 250)
	push(e, "r1", "dana", "m1", "line1\nline2\t\x1b[31mred\x1b[0m \x07bell  "+long, 0)
	sentinel(t, e, rec)
	got := dropSentinels(rec.osCalls())
	want := "line1 line2 red bell " + strings.Repeat("é", 200-len("line1 line2 red bell ")) + "…"
	if len(got) != 1 {
		t.Fatalf("got %+v, want one notification", got)
	}
	if got[0].Text != want {
		t.Errorf("Text = %q\nwant   %q", got[0].Text, want)
	}
	if got[0].ConvName != "platform" || got[0].Sender != "Dana" || got[0].DM || got[0].Mention {
		t.Errorf("payload = %+v", got[0])
	}
}

func TestDuplicateEventNotifiesOnce(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	push(e, "d1", "dana", "m1", "once", 1)
	push(e, "d1", "dana", "m1", "once", 1)
	sentinel(t, e, rec)
	if got := texts(dropSentinels(rec.osCalls())); !slices.Equal(got, []string{"once"}) {
		t.Fatalf("notified %q, want exactly one", got)
	}
}

func TestEditsDeletesAndMembershipNeverNotify(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Rooms["r1"] = "all"
	started(t, e, room("r1", "platform", "me", "dana"))
	push(e, "r1", "dana", "m1", "first", 0)
	waitUntil(t, "first", func() bool { return len(rec.osCalls()) == 1 })
	e.fake.Push(model.Event{ConvID: "r1", Kind: model.MessageEdited, Actor: "dana", MsgID: "m1", Body: &model.Body{Format: "text/plain", Data: []byte("edited")}})
	e.fake.Push(model.Event{ConvID: "r1", Kind: model.MemberJoined, Actor: "dana", Target: "sam"})
	e.fake.Push(model.Event{ConvID: "r1", Kind: model.MessageDeleted, Actor: "dana", MsgID: "m1"})
	push(e, "r1", "dana", "m2", "deleted at once", 0)
	e.fake.Push(model.Event{ConvID: "r1", Kind: model.MessageDeleted, Actor: "dana", MsgID: "m2"})
	sentinel(t, e, rec)
	got := texts(dropSentinels(rec.osCalls()))
	if len(got) < 1 || got[0] != "first" {
		t.Fatalf("notified %q", got)
	}
	// m2 may notify before its delete is read; it must not do so twice, and
	// nothing else may notify.
	if len(got) > 2 || len(got) == 2 && got[1] != "deleted at once" {
		t.Fatalf("notified %q, want only the two posts", got)
	}
}

func TestOwnMessageNeverNotifies(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	push(e, "d1", "me", "m1", "from my other device", 0)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("notified %+v for the user's own message", got)
	}
}

func TestRoomModes(t *testing.T) {
	for _, tc := range []struct {
		mode                   string
		wantPlain, wantMention bool
	}{
		{"", false, true},
		{"mentions", false, true},
		{"all", true, true},
		{"muted", false, false},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			e, rec := noteEnv(t)
			if tc.mode != "" {
				e.cfg.Rooms["r1"] = tc.mode
			}
			started(t, e, room("r1", "platform", "me", "dana"))
			push(e, "r1", "dana", "plain", "plain", 0)
			push(e, "r1", "dana", "ment", "ment", 0, "me")
			push(e, "r1", "dana", "other", "other", 0, "sam")
			sentinel(t, e, rec)
			var want []string
			if tc.wantPlain {
				want = append(want, "plain")
			}
			if tc.wantMention {
				want = append(want, "ment")
			}
			if tc.wantPlain {
				want = append(want, "other")
			}
			if got := texts(dropSentinels(rec.osCalls())); !slices.Equal(got, want) {
				t.Fatalf("notified %q, want %q", got, want)
			}
		})
	}
}

func TestMutedRoomDoesNotAffectDMs(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Rooms["d1"] = "muted"
	started(t, e, dm("d1", "dana"))
	push(e, "d1", "dana", "m1", "still a dm", 0)
	sentinel(t, e, rec)
	if got := texts(dropSentinels(rec.osCalls())); !slices.Equal(got, []string{"still a dm"}) {
		t.Fatalf("notified %q", got)
	}
}

func TestReadOnAnotherDeviceBeforeItWouldNotify(t *testing.T) {
	e, rec := noteEnv(t)
	d1 := dm("d1", "dana")
	d1.LastSeq, d1.ReadSeq = 5, 5
	started(t, e, d1)
	push(e, "d1", "dana", "old", "already read elsewhere", 4)
	push(e, "d1", "dana", "new", "unread", 6)
	sentinel(t, e, rec)
	if got := texts(dropSentinels(rec.osCalls())); !slices.Equal(got, []string{"unread"}) {
		t.Fatalf("notified %q, want only the message after the read position", got)
	}
}

func TestCatchUpNotifiesOncePerConversationNewestQualifying(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Rooms["r1"] = "mentions"
	e.cfg.Rooms["r2"] = "muted"
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 2)...)
	// A conversation with nothing held starts from History, which never
	// notifies, so d1 already holds a message when the user goes away.
	e.fake.AddConversation(dm("d1", "dana"), messages("x", 1)...)
	e.fake.AddConversation(room("r2", "quiet", "me", "dana"))
	c := startedWith(t, e, 3)
	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	// While away: r1 gets four plain messages and, third, a mention; d1 gets
	// three; r2 (muted) gets two.
	r1 := room("r1", "platform", "me", "dana")
	e.fake.AddConversation(r1, push0("dana", "r1-1", "r1 one"), push0("dana", "r1-2", "r1 two"),
		model.Event{Kind: model.MessagePosted, Actor: "dana", MsgID: "r1-3", Mentions: []string{"me"}, Body: &model.Body{Format: "text/plain", Data: []byte("r1 three")}},
		push0("dana", "r1-4", "r1 four"))
	e.fake.AddConversation(dm("d1", "dana"), messages("d", 3)...)
	e.fake.AddConversation(room("r2", "quiet", "me", "dana"), messages("q", 2)...)
	e.fake.SetPageSize(2)
	defer runClock(e)()
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "caught up", func() bool {
		msgs, _ := e.st.Messages("d1", 0, 100)
		st, _ := c.Status()
		return len(seqs(msgs)) == 4 && !st.Syncing && st.Conn == connector.StateConnected
	})
	sentinel(t, e, rec)
	got := dropSentinels(rec.osCalls())
	want := map[string]string{"r1": "r1 three", "d1": "d message 3"}
	if len(got) != len(want) {
		t.Fatalf("notified %+v, want one each for %v", got, want)
	}
	for _, p := range got {
		if want[p.ConvID] != p.Text {
			t.Errorf("conversation %s notified %q, want %q", p.ConvID, p.Text, want[p.ConvID])
		}
	}
}

func push0(author, msgID, text string) model.Event {
	return model.Event{Kind: model.MessagePosted, Actor: author, MsgID: msgID, Body: &model.Body{Format: "text/plain", Data: []byte(text)}}
}

func TestBackfillAndFirstSyncNeverNotify(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Rooms["r1"] = "all"
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 120)...)
	e.fake.AddConversation(dm("d1", "dana"), messages("b", 60)...)
	c := e.start()
	waitUntil(t, "conversations", func() bool { return len(chatIDs(t, c)) == 2 })
	defer runClock(e)()
	// Backfill fetches the older pages of each; wait for all of them.
	waitUntil(t, "backfill", func() bool {
		st, _ := c.Status()
		r1, _ := e.st.Messages("r1", 0, 500)
		d1, _ := e.st.Messages("d1", 0, 500)
		return len(seqs(r1)) == 120 && len(seqs(d1)) == 60 && !st.Syncing
	})
	if got := rec.osCalls(); len(got) != 0 {
		t.Fatalf("history raised notifications: %+v", got)
	}
}

// runClock keeps the backfill running flat out until the returned function is
// called.
func runClock(e *env) func() {
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				e.clock.Advance(backfillInterval)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	return func() { close(stop) }
}

func TestRoomModeChangeDecidesNextMessage(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, room("r1", "platform", "me", "dana"))
	push(e, "r1", "dana", "m1", "before", 0)
	sentinel(t, e, rec)
	if got := texts(dropSentinels(rec.osCalls())); len(got) != 0 {
		t.Fatalf("notified %q at the default mode", got)
	}
	if _, err := e.daemon().updateConfig(func(c *config.Config) error { return c.Set(config.RoomPrefix+"r1", "all") }); err != nil {
		t.Fatal(err)
	}
	push(e, "r1", "dana", "m2", "after", 0)
	sentinel(t, e, rec)
	if got := texts(dropSentinels(rec.osCalls())); !slices.Equal(got, []string{"after"}) {
		t.Fatalf("notified %q, want only the message after the change", got)
	}
}
