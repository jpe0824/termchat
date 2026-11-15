package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

func seqs(ms []Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.Seq
	}
	return out
}

func fill(t *testing.T, s *Store, conv string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		must(t, noErr(s.Apply(posted(conv, int64(i), fmt.Sprintf("m%d", i), "ann", fmt.Sprintf("text %d", i)))))
	}
}

func TestMessagesPagingAscending(t *testing.T) {
	s := openTest(t)
	fill(t, s, "c1", 10)
	got, err := s.Messages("c1", 0, 3)
	must(t, err)
	if !reflect.DeepEqual(seqs(got), []int64{8, 9, 10}) {
		t.Fatalf("newest page = %v", seqs(got))
	}
	got, err = s.Messages("c1", 8, 3)
	must(t, err)
	if !reflect.DeepEqual(seqs(got), []int64{5, 6, 7}) {
		t.Fatalf("page before 8 = %v", seqs(got))
	}
	got, err = s.Messages("c1", 3, 50)
	must(t, err)
	if !reflect.DeepEqual(seqs(got), []int64{1, 2}) {
		t.Fatalf("page before 3 = %v", seqs(got))
	}
	got, err = s.Messages("c1", 1, 5)
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("page before 1 = %v", seqs(got))
	}
}

func TestMessagesDefaultLimitIsFifty(t *testing.T) {
	s := openTest(t)
	fill(t, s, "c1", 60)
	for _, limit := range []int{0, -1} {
		got, err := s.Messages("c1", 0, limit)
		must(t, err)
		if len(got) != 50 || got[0].Seq != 11 || got[49].Seq != 60 {
			t.Fatalf("limit %d: %d messages from %d", limit, len(got), got[0].Seq)
		}
	}
}

func TestMessagesAreScopedToTheConversation(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "one"), posted("c2", 1, "m1", "ann", "two"))))
	if got := texts(msgs(t, s, "c1")); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("c1 = %v", got)
	}
	if got := texts(msgs(t, s, "c2")); !reflect.DeepEqual(got, []string{"two"}) {
		t.Fatalf("c2 = %v", got)
	}
}

func TestOneEntryPerMessageDespiteEdits(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(
		posted("c1", 1, "m1", "ann", "a"),
		edited("c1", 2, "m1", "ann", "b"),
		edited("c1", 3, "m1", "ann", "c"),
		posted("c1", 4, "m2", "ann", "other"),
	)))
	got := msgs(t, s, "c1")
	if len(got) != 2 || got[0].Text != "c" || got[0].Seq != 1 || got[1].Seq != 4 {
		t.Fatalf("Messages = %+v", got)
	}
}

func TestMessageAgentAndNameComeFromPeople(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertPeople([]model.Person{
		{Handle: "ann", DisplayName: "Ann Lee"},
		{Handle: "robo", DisplayName: "Robo", Agent: true, Owner: "ann"},
		{Handle: "plain"},
	}))
	must(t, noErr(s.Apply(
		posted("c1", 1, "m1", "ann", "a"),
		posted("c1", 2, "m2", "robo", "b"),
		posted("c1", 3, "m3", "ghost", "c"),
		posted("c1", 4, "m4", "plain", "d"),
	)))
	got := msgs(t, s, "c1")
	type who struct {
		author, name string
		agent        bool
	}
	want := []who{{"ann", "Ann Lee", false}, {"robo", "Robo", true}, {"ghost", "ghost", false}, {"plain", "plain", false}}
	for i, w := range want {
		if g := (who{got[i].Author, got[i].AuthorName, got[i].Agent}); g != w {
			t.Errorf("message %d = %+v, want %+v", i, g, w)
		}
	}
	// Learning about the author later applies to messages already stored.
	must(t, s.UpsertPeople([]model.Person{{Handle: "ghost", DisplayName: "Casper", Agent: true}}))
	if g := msgs(t, s, "c1")[2]; g.AuthorName != "Casper" || !g.Agent {
		t.Fatalf("late people update not reflected: %+v", g)
	}
}

func TestByMsgID(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "a"), edited("c1", 2, "m1", "ann", "b"))))
	m, err := s.ByMsgID("c1", "m1")
	must(t, err)
	if m.Text != "b" || !m.Edited || m.Seq != 1 || m.ConvID != "c1" {
		t.Fatalf("ByMsgID = %+v", m)
	}
	if _, err := s.ByMsgID("c1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing message error = %v, want ErrNotFound", err)
	}
	if _, err := s.ByMsgID("c2", "m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong conversation error = %v, want ErrNotFound", err)
	}
}

func TestByMsgIDFindsOutboxItems(t *testing.T) {
	s := openTest(t)
	must(t, s.Enqueue(OutboxItem{MsgID: "q1", ConvID: "c1", Text: "queued", Source: "cli"}))
	m, err := s.ByMsgID("c1", "q1")
	must(t, err)
	if m.Text != "queued" || m.Status != StatusSending || m.Author != "me" || m.Seq != 0 {
		t.Fatalf("ByMsgID = %+v", m)
	}
}
