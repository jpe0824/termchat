package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func enqueue(t *testing.T, s *Store, conv, msgID, text string) {
	t.Helper()
	must(t, s.Enqueue(OutboxItem{MsgID: msgID, ConvID: conv, Text: text, Source: "tui"}))
}

func msgIDs(items []OutboxItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.MsgID
	}
	return out
}

func TestOutboxMergedIntoMessages(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "stored one"), posted("c1", 2, "m2", "ann", "stored two")))
	must(t, s.Enqueue(OutboxItem{MsgID: "q1", ConvID: "c1", Text: "first queued @bob", Mentions: []string{"bob"}, Source: "tui"}))
	enqueue(t, s, "c1", "q2", "second queued")
	enqueue(t, s, "c2", "q3", "other conversation")

	got := msgs(t, s, "c1")
	if want := []string{"stored one", "stored two", "first queued @bob", "second queued"}; !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("Messages = %v, want %v", texts(got), want)
	}
	if got[1].Status != "" || got[2].Status != StatusSending || got[3].Status != StatusSending {
		t.Fatalf("statuses = %q %q %q", got[1].Status, got[2].Status, got[3].Status)
	}
	if got[2].Author != "me" || got[2].Seq != 0 || got[2].MsgID != "q1" || !reflect.DeepEqual(got[2].Mentions, []string{"bob"}) || got[2].ConvID != "c1" {
		t.Fatalf("outbox message = %+v", got[2])
	}
	// Paging backwards never repeats the outbox.
	older, err := s.Messages("c1", 2, 10)
	must(t, err)
	if !reflect.DeepEqual(texts(older), []string{"stored one"}) {
		t.Fatalf("older page = %v", texts(older))
	}
	must(t, s.MarkFailed("q2", "offline"))
	if got := msgs(t, s, "c1"); got[3].Status != StatusFailed {
		t.Fatalf("failed item status = %q", got[3].Status)
	}
}

func TestPostedEventClearsOutbox(t *testing.T) {
	s := openTest(t)
	enqueue(t, s, "c1", "q1", "hello")
	enqueue(t, s, "c1", "q2", "stuck")
	must(t, s.MarkFailed("q2", "boom"))
	must(t, s.Apply(posted("c1", 1, "q1", "me", "hello"), posted("c1", 2, "q2", "me", "stuck")))
	if p, err := s.Pending(); err != nil || len(p) != 0 {
		t.Fatalf("Pending = %v, %v", p, err)
	}
	if all, err := s.Outbox("c1"); err != nil || len(all) != 0 {
		t.Fatalf("Outbox = %v, %v (a failed item must clear too)", all, err)
	}
	got := msgs(t, s, "c1")
	if len(got) != 2 || got[0].Status != "" || got[1].Status != "" {
		t.Fatalf("message appears twice or still pending: %+v", got)
	}
}

func TestEditOrDeleteEventsLeaveOutboxAlone(t *testing.T) {
	s := openTest(t)
	enqueue(t, s, "c1", "q1", "hello")
	must(t, s.Apply(edited("c1", 2, "q1", "me", "later")))
	if p, _ := s.Pending(); len(p) != 1 {
		t.Fatalf("an edit event cleared the outbox: %v", p)
	}
}

func TestOutboxSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "acct")
	must(t, err)
	must(t, s.SetMe("me"))
	must(t, s.Enqueue(OutboxItem{MsgID: "q1", ConvID: "c1", Text: "keep me", Mentions: []string{"ann", "bob"}, Source: "cli"}))
	enqueue(t, s, "c1", "q2", "fail me")
	must(t, s.MarkFailed("q2", "no route"))
	must(t, s.Close())

	s, err = Open(dir, "acct")
	must(t, err)
	defer func() { _ = s.Close() }()
	p, err := s.Pending()
	must(t, err)
	if len(p) != 1 || p[0].MsgID != "q1" || p[0].Text != "keep me" || p[0].Source != "cli" ||
		p[0].State != "sending" || !reflect.DeepEqual(p[0].Mentions, []string{"ann", "bob"}) || p[0].ConvID != "c1" {
		t.Fatalf("Pending after reopen = %+v", p)
	}
	all, err := s.Outbox("c1")
	must(t, err)
	if len(all) != 2 || all[1].State != "failed" || all[1].Err != "no route" {
		t.Fatalf("Outbox after reopen = %+v", all)
	}
}

func TestEnqueueRejectsEmptyAndDuplicateMsgID(t *testing.T) {
	s := openTest(t)
	if err := s.Enqueue(OutboxItem{ConvID: "c1", Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty MsgID error = %v, want ErrInvalid", err)
	}
	if err := s.Enqueue(OutboxItem{MsgID: "q1", Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty ConvID error = %v, want ErrInvalid", err)
	}
	enqueue(t, s, "c1", "q1", "first")
	if err := s.Enqueue(OutboxItem{MsgID: "q1", ConvID: "c2", Text: "second"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate error = %v, want ErrExists", err)
	}
	if p, _ := s.Pending(); len(p) != 1 || p[0].Text != "first" {
		t.Fatalf("Pending = %+v", p)
	}
}

func TestPendingOrderAndStates(t *testing.T) {
	s := openTest(t)
	same := t0.Add(time.Hour)
	must(t, s.Enqueue(OutboxItem{MsgID: "b", ConvID: "c1", Text: "b", CreatedAt: same}))
	must(t, s.Enqueue(OutboxItem{MsgID: "a", ConvID: "c1", Text: "a", CreatedAt: same}))
	must(t, s.Enqueue(OutboxItem{MsgID: "early", ConvID: "c2", Text: "e", CreatedAt: t0}))
	enqueue(t, s, "c1", "dflt", "uses clock")
	must(t, s.MarkFailed("a", "x"))

	p, err := s.Pending()
	must(t, err)
	// "dflt" takes its time from the store clock, which is earlier than `same`;
	// "a" is failed and so absent.
	if want := []string{"early", "dflt", "b"}; !reflect.DeepEqual(msgIDs(p), want) {
		t.Fatalf("Pending = %v, want %v", msgIDs(p), want)
	}
	all, err := s.Outbox("c1")
	must(t, err)
	if len(all) != 3 {
		t.Fatalf("Outbox(c1) = %v", msgIDs(all))
	}
}

func TestPendingTiesBreakByInsertionOrder(t *testing.T) {
	s := openTest(t)
	for _, id := range []string{"z", "y", "x"} {
		must(t, s.Enqueue(OutboxItem{MsgID: id, ConvID: "c1", Text: id, CreatedAt: t0}))
	}
	p, err := s.Pending()
	must(t, err)
	if !reflect.DeepEqual(msgIDs(p), []string{"z", "y", "x"}) {
		t.Fatalf("Pending = %v", msgIDs(p))
	}
}

func TestMarkFailedRequeueDiscard(t *testing.T) {
	s := openTest(t)
	enqueue(t, s, "c1", "q1", "hello")
	enqueue(t, s, "c1", "q2", "world")
	before, _ := s.Outbox("c1")

	must(t, s.MarkFailed("q1", "server said no"))
	p, _ := s.Pending()
	if !reflect.DeepEqual(msgIDs(p), []string{"q2"}) {
		t.Fatalf("Pending after MarkFailed = %v", msgIDs(p))
	}
	all, _ := s.Outbox("c1")
	if all[0].State != "failed" || all[0].Err != "server said no" {
		t.Fatalf("failed item = %+v", all[0])
	}

	must(t, s.Requeue("q1"))
	all, _ = s.Outbox("c1")
	if all[1].MsgID != "q1" || all[1].State != "sending" || all[1].Err != "" {
		t.Fatalf("requeued item = %+v", all[1])
	}
	if !all[1].CreatedAt.After(before[1].CreatedAt) {
		t.Fatalf("Requeue kept CreatedAt %v, want a time after %v", all[1].CreatedAt, before[1].CreatedAt)
	}
	p, _ = s.Pending()
	if !reflect.DeepEqual(msgIDs(p), []string{"q2", "q1"}) {
		t.Fatalf("Pending after Requeue = %v (a requeued item goes to the back)", msgIDs(p))
	}

	must(t, s.Discard("q2"))
	p, _ = s.Pending()
	if !reflect.DeepEqual(msgIDs(p), []string{"q1"}) {
		t.Fatalf("Pending after Discard = %v", msgIDs(p))
	}
	if hits := tablesHolding(t, s, "world"); len(hits) != 0 {
		t.Fatalf("discarded text still held in %v", hits)
	}
}

func TestOutboxMutationsOnMissingItems(t *testing.T) {
	s := openTest(t)
	for name, err := range map[string]error{
		"MarkFailed": s.MarkFailed("nope", "x"),
		"Requeue":    s.Requeue("nope"),
		"Discard":    s.Discard("nope"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s = %v, want ErrNotFound", name, err)
		}
	}
}

func TestRequeueOfSendingItemIsNoOp(t *testing.T) {
	s := openTest(t)
	enqueue(t, s, "c1", "q1", "hello")
	before, _ := s.Outbox("c1")
	must(t, s.Requeue("q1"))
	after, _ := s.Outbox("c1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Requeue changed a sending item: %+v -> %+v", before, after)
	}
}

func TestPostedEventInAnotherConversationLeavesOutbox(t *testing.T) {
	s := openTest(t)
	enqueue(t, s, "c1", "x", "unsent")
	must(t, s.Apply(posted("c2", 1, "x", "me", "same id elsewhere")))
	p, err := s.Pending()
	must(t, err)
	if len(p) != 1 || p[0].ConvID != "c1" || p[0].MsgID != "x" {
		t.Fatalf("Pending = %+v, want the c1 item kept", p)
	}
}

func TestPostedEventFromAnotherActorLeavesOutbox(t *testing.T) {
	s := openTest(t)
	enqueue(t, s, "c1", "x", "unsent")
	must(t, s.Apply(posted("c1", 1, "x", "ann", "someone else's")))
	if p, err := s.Pending(); err != nil || len(p) != 1 {
		t.Fatalf("Pending = %+v, %v, want the item kept", p, err)
	}
	// The server will reject our duplicate; the user must be able to see that.
	must(t, s.MarkFailed("x", "duplicate message id"))
	all, err := s.Outbox("c1")
	must(t, err)
	if len(all) != 1 || all[0].State != StatusFailed {
		t.Fatalf("Outbox = %+v", all)
	}
}

func TestPostedEventKeepsOutboxWhenMeIsUnset(t *testing.T) {
	s, err := Open(t.TempDir(), "acct")
	must(t, err)
	defer func() { _ = s.Close() }()
	enqueue(t, s, "c1", "x", "unsent")
	must(t, s.Apply(posted("c1", 1, "x", "me", "hello")))
	if p, err := s.Pending(); err != nil || len(p) != 1 {
		t.Fatalf("Pending = %+v, %v, want the item kept while Me is unset", p, err)
	}
}
