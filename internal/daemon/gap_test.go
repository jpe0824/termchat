package daemon

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
)

// r1Synced starts the daemon on r1 with n messages and waits for the first
// catch-up to end.
func r1Synced(t *testing.T, e *env, rec *recorder, n int, members ...string) {
	t.Helper()
	e.fake.AddConversation(room("r1", "platform", members...), messages("a", n)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rec.syncCalls()) == 1 })
}

func in(conv string, ev model.Event) model.Event {
	ev.ConvID = conv
	return ev
}

func members(t *testing.T, e *env, conv string) []string {
	t.Helper()
	v, err := e.st.ConvView(conv)
	if err != nil {
		return nil
	}
	return v.Members
}

// A removal written to the server's database by the admin command in another
// process is never pushed. The next live event in that room arrives past the
// gap, and the daemon fetches what it missed without waiting for a reconnect.
func TestLiveEventPastAGapSyncsTheConversation(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	r1Synced(t, e, rec, 3, "me", "dana", "eve")

	e.fake.AddConversation(room("r1", "platform", "me", "dana"),
		model.Event{Kind: model.MemberLeft, Actor: "eve", Target: "eve"}) // Seq 4, not pushed
	e.fake.Push(in("r1", posted("dana", "b-1", "after the removal"))) // Seq 5

	waitUntil(t, "the gap filled", func() bool { return e.st.Cursor("r1") == 5 })
	if got := members(t, e, "r1"); slices.Contains(got, "eve") {
		t.Fatalf("members = %v, eve was removed", got)
	}
	calls := rec.syncCalls()
	if len(calls) != 2 || !maps1(calls[1], "r1", 3) {
		t.Fatalf("Sync calls = %v, want the catch-up then r1 from 3", calls)
	}
	if st, _ := e.c.Status(); st.Conn != connector.StateConnected {
		t.Fatalf("state %s; the repair must not need a reconnect", st.Conn)
	}
}

// The server leaves a room change's own events out of the pushes to the
// device that made it; the daemon fetches them after the change.
func TestOwnRoomChangeEventsAreFetched(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	r1Synced(t, e, rec, 2, "me", "dana")

	if _, err := e.c.Room(connector.RoomOp{Kind: connector.RoomInvite, Conv: "r1", Handle: "bob"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the member.joined event held", func() bool { return e.st.Cursor("r1") == 3 })
	if got := members(t, e, "r1"); !slices.Contains(got, "bob") {
		t.Fatalf("members = %v, want bob", got)
	}
}

// A live event the store fails to write, for a reason other than the event
// being malformed, is fetched again by a sync, and retried later while the
// store keeps failing.
func TestFailedLiveWriteIsSyncedAndRetried(t *testing.T) {
	e := newEnv(t)
	logs := captureLogs(e)
	rec := record(e)
	r1Synced(t, e, rec, 3, "me", "dana")

	db, err := sql.Open("sqlite", filepath.Join(e.dataDir, "accounts", "me", "client.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_poison BEFORE INSERT ON events WHEN NEW.msg_id = 'poison'
		BEGIN SELECT RAISE(ABORT, 'simulated storage failure'); END`); err != nil {
		t.Fatal(err)
	}

	e.fake.Push(in("r1", posted("dana", "poison", "kept by the server"))) // Seq 4
	// Once live, once by the repair's sync, which then waits to retry.
	waitLogged(t, logs, "skipping event the store rejected", 2)
	waitLogged(t, logs, "gap repair incomplete", 1)
	if calls := rec.syncCalls(); len(calls) != 2 || !maps1(calls[1], "r1", 3) {
		t.Fatalf("Sync calls = %v, want the catch-up then r1 from 3", calls)
	}
	if e.st.Cursor("r1") != 3 {
		t.Fatalf("cursor %d, want 3 while the store fails", e.st.Cursor("r1"))
	}

	if _, err := db.Exec(`DROP TRIGGER fail_poison`); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(resyncAfter)
	waitUntil(t, "the event stored on the retry", func() bool { return e.st.Cursor("r1") == 4 })
	if m, err := e.st.ByMsgID("r1", "poison"); err != nil || m.Seq != 4 {
		t.Fatalf("ByMsgID = %+v, %v", m, err)
	}
}
