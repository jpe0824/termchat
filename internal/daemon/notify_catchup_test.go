package daemon

import (
	"slices"
	"testing"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// syncedOnce starts a daemon whose account already completed a catch-up with
// one held conversation, then takes it offline.
func syncedOnce(t *testing.T) (*env, *noteRec, *ipc.Client) {
	t.Helper()
	e, rec := noteEnv(t)
	e.fake.AddConversation(dm("d1", "dana"), messages("x", 1)...)
	c := startedWith(t, e, 1)
	waitCaughtUp(t, c)
	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	return e, rec, c
}

func waitCaughtUp(t *testing.T, c *ipc.Client) {
	t.Helper()
	waitUntil(t, "caught up", func() bool {
		st, _ := c.Status()
		return st.Conn == connector.StateConnected && !st.Syncing
	})
}

// reconnectWith adds conv with events while offline, reconnects, and waits
// until the catch-up that found it is over.
func reconnectWith(t *testing.T, e *env, c *ipc.Client, conv model.Conversation, evs ...model.Event) {
	t.Helper()
	e.fake.AddConversation(conv, evs...)
	defer runClock(e)()
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "new conversation", func() bool { return slices.Contains(chatIDs(t, c), conv.ID) })
	waitCaughtUp(t, c)
}

func TestNewDMWhileOfflineNotifiesOnceForTheNewest(t *testing.T) {
	e, rec, c := syncedOnce(t)
	reconnectWith(t, e, c, dm("d2", "dana"), messages("n", 2)...)
	sentinel(t, e, rec)
	got := dropSentinels(rec.osCalls())
	want := []ipc.NotifyPayload{{ConvID: "d2", ConvName: "dana", Sender: "Dana", Text: "n message 2", DM: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("notified %+v, want %+v", got, want)
	}
}

func TestNewDMAlreadyReadElsewhereStaysSilent(t *testing.T) {
	e, rec, c := syncedOnce(t)
	d2 := dm("d2", "dana")
	d2.ReadSeq = 2
	reconnectWith(t, e, c, d2, messages("n", 2)...)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("notified %+v for messages read on another device", got)
	}
}

func TestNewRoomFirstFillFollowsRoomMode(t *testing.T) {
	e, rec, c := syncedOnce(t)
	reconnectWith(t, e, c, room("r2", "plain", "me", "dana"), messages("n", 2)...)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("notified %+v for an unmentioned room message in mentions mode", got)
	}

	mention := model.Event{Kind: model.MessagePosted, Actor: "dana", MsgID: "mm", Mentions: []string{"me"},
		Body: &model.Body{Format: "text/plain", Data: []byte("hey you")}}
	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	events := append(messages("m", 1), mention, push0("dana", "after", "later, no mention"))
	reconnectWith(t, e, c, room("r3", "mentioned", "me", "dana"), events...)
	sentinel(t, e, rec)
	got := dropSentinels(rec.osCalls())
	if len(got) != 1 || got[0].ConvID != "r3" || got[0].Text != "hey you" || !got[0].Mention {
		t.Fatalf("notified %+v, want one mention from r3", got)
	}
}

func TestFirstEverCatchUpAndRestartStaySilent(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Rooms["r1"] = "all"
	e.fake.AddConversation(dm("d1", "dana"), messages("a", 3)...)
	e.fake.AddConversation(dm("d2", "sam"), messages("b", 3)...)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("c", 3)...)
	stop := runClock(e)
	c := startedWith(t, e, 3)
	waitCaughtUp(t, c)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("first catch-up announced the backlog: %+v", got)
	}
	stop()

	e.stop()
	c = e.start()
	waitCaughtUp(t, c)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("restart announced old messages: %+v", got)
	}
}
