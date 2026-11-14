package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

func room(id, name string) model.Conversation {
	return model.Conversation{ID: id, Kind: model.ConvRoom, Name: name}
}

func convs(t *testing.T, s *Store) []ConvView {
	t.Helper()
	cs, err := s.Conversations()
	must(t, err)
	return cs
}

func ids(cs []ConvView) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func TestUpsertConversationRoundTrip(t *testing.T) {
	s := openTest(t)
	in := model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", Open: true, Creator: "ann",
		Members: []string{"bob", "ann"}, LastSeq: 12, ReadSeq: 4}
	must(t, s.UpsertConversation(in))
	got := convs(t, s)
	if len(got) != 1 {
		t.Fatalf("Conversations = %+v", got)
	}
	want := in
	want.Members = []string{"ann", "bob"}
	if !reflect.DeepEqual(got[0].Conversation, want) {
		t.Fatalf("Conversation = %+v, want %+v", got[0].Conversation, want)
	}
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng2", LastSeq: 15}))
	got = convs(t, s)
	if got[0].Name != "eng2" || got[0].LastSeq != 15 || got[0].Open || len(got[0].Members) != 0 {
		t.Fatalf("after second upsert = %+v", got[0].Conversation)
	}
}

func TestConversationsOrderedByLastActivity(t *testing.T) {
	s := openTest(t)
	for _, c := range []model.Conversation{room("quiet-b", "b"), room("quiet-a", "a"), room("old", "z"), room("new", "y")} {
		must(t, s.UpsertConversation(c))
	}
	must(t, noErr(s.Apply(
		posted("old", 1, "m1", "ann", "x"),
		posted("new", 9, "m2", "ann", "y"),
		member("quiet-a", 20, model.MemberJoined, "ann"), // membership is not activity
	)))
	got := convs(t, s)
	if want := []string{"new", "old", "quiet-a", "quiet-b"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("order = %v, want %v", ids(got), want)
	}
	if !got[0].LastAt.Equal(evAt(9)) || !got[2].LastAt.IsZero() {
		t.Fatalf("LastAt = %v / %v", got[0].LastAt, got[2].LastAt)
	}
}

func TestEditCountsAsActivity(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(room("a", "a")))
	must(t, s.UpsertConversation(room("b", "b")))
	must(t, noErr(s.Apply(posted("a", 1, "m1", "ann", "x"), posted("b", 2, "m2", "ann", "y"), edited("a", 3, "m1", "ann", "x2"))))
	if got := ids(convs(t, s)); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestUnreadCountsFromReadSeq(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", ReadSeq: 1}))
	must(t, noErr(s.Apply(
		posted("c1", 1, "m1", "ann", "read already"),
		posted("c1", 2, "m2", "me", "my own"),
		posted("c1", 3, "m3", "ann", "unread"),
		posted("c1", 4, "m4", "bob", "also unread"),
		deleted("c1", 5, "m4", "bob"),
		member("c1", 6, model.MemberJoined, "cat"),
		posted("c1", 7, "m5", "bob", "last"),
	)))
	if got := convs(t, s)[0].Unread; got != 2 {
		t.Fatalf("Unread = %d, want 2 (seq 3 and 7)", got)
	}
	must(t, s.SetRead("c1", 7))
	if got := convs(t, s)[0].Unread; got != 0 {
		t.Fatalf("Unread after SetRead = %d, want 0", got)
	}
}

func TestLastText(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(room("c1", "eng")))
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "first"), posted("c1", 2, "m2", "bob", "second"))))
	if got := convs(t, s)[0].LastText; got != "second" {
		t.Fatalf("LastText = %q", got)
	}
	must(t, noErr(s.Apply(edited("c1", 3, "m2", "bob", "second v2"))))
	if got := convs(t, s)[0].LastText; got != "second v2" {
		t.Fatalf("LastText after edit = %q", got)
	}
	must(t, noErr(s.Apply(deleted("c1", 4, "m2", "bob"))))
	if got := convs(t, s)[0].LastText; got != "" {
		t.Fatalf("LastText after delete = %q, want empty", got)
	}
}

func TestSetReadNeverMovesBackwards(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(room("c1", "eng")))
	must(t, s.SetRead("c1", 8))
	must(t, s.SetRead("c1", 3))
	if got := convs(t, s)[0].ReadSeq; got != 8 {
		t.Fatalf("ReadSeq = %d, want 8", got)
	}
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", ReadSeq: 5}))
	if got := convs(t, s)[0].ReadSeq; got != 8 {
		t.Fatalf("ReadSeq after stale upsert = %d, want 8", got)
	}
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", ReadSeq: 11}))
	if got := convs(t, s)[0].ReadSeq; got != 11 {
		t.Fatalf("ReadSeq after newer upsert = %d, want 11", got)
	}
	if err := s.SetRead("nope", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRead on unknown conversation = %v, want ErrNotFound", err)
	}
}

func TestGoneConversationsAreHiddenButKept(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(room("c1", "eng")))
	must(t, s.UpsertConversation(room("c2", "ops")))
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "history stays"))))
	must(t, s.MarkGone("c1"))
	if got := ids(convs(t, s)); !reflect.DeepEqual(got, []string{"c2"}) {
		t.Fatalf("Conversations = %v, want only c2", got)
	}
	if got := msgs(t, s, "c1"); len(got) != 1 || got[0].Text != "history stays" {
		t.Fatalf("history of a gone conversation = %+v", got)
	}
	// Re-joined: the server lists it again.
	must(t, s.UpsertConversation(room("c1", "eng")))
	if got := ids(convs(t, s)); len(got) != 2 {
		t.Fatalf("Conversations after rejoin = %v", got)
	}
	if err := s.MarkGone("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkGone on unknown conversation = %v, want ErrNotFound", err)
	}
}

func TestOwnMemberLeftMarksGoneAndOthersDoNot(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", Members: []string{"me", "ann"}, LastSeq: 2}))
	must(t, noErr(s.Apply(member("c1", 3, model.MemberLeft, "ann"))))
	if len(convs(t, s)) != 1 {
		t.Fatal("another member leaving hid the conversation")
	}
	must(t, noErr(s.Apply(member("c1", 4, model.MemberLeft, "me"))))
	if len(convs(t, s)) != 0 {
		t.Fatal("own member.left did not hide the conversation")
	}
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", Members: []string{"me"}, LastSeq: 6}))
	if len(convs(t, s)) != 1 {
		t.Fatal("re-upsert did not clear the gone mark")
	}
}

func TestOwnMemberLeftOlderThanSnapshotIsIgnored(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", Members: []string{"me"}, LastSeq: 10}))
	// A history page brings an old leave-and-rejoin the snapshot already reflects.
	must(t, noErr(s.Apply(member("c1", 4, model.MemberLeft, "me"))))
	if len(convs(t, s)) != 1 {
		t.Fatal("a leave older than the snapshot hid the conversation")
	}
}

func TestOwnLeaveThenRejoinEventsAreOrderIndependent(t *testing.T) {
	for name, order := range map[string][]int{"asc": {0, 1}, "desc": {1, 0}} {
		t.Run(name, func(t *testing.T) {
			s := openTest(t)
			must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "eng", LastSeq: 2}))
			evs := []model.Event{member("c1", 3, model.MemberLeft, "me"), member("c1", 4, model.MemberJoined, "me")}
			for _, i := range order {
				must(t, noErr(s.Apply(evs[i])))
			}
			if len(convs(t, s)) != 1 {
				t.Fatal("conversation hidden although the newest event is a join")
			}
		})
	}
}

func TestMembersAndNameFollowHighestSeqRegardlessOfOrder(t *testing.T) {
	evs := []model.Event{
		member("c1", 11, model.MemberJoined, "cat"),
		member("c1", 12, model.MemberLeft, "ann"),
		member("c1", 13, model.MemberJoined, "ann"),
		member("c1", 14, model.MemberLeft, "cat"),
		member("c1", 15, model.MemberJoined, "cat"),
		renamed("c1", 12, "mid"),
		renamed("c1", 16, "final"),
		// Older than the snapshot: already reflected, must not undo it.
		member("c1", 4, model.MemberLeft, "bob"),
		renamed("c1", 3, "ancient"),
	}
	snapshot := model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "start", Members: []string{"ann", "bob"}, LastSeq: 10}
	for name, order := range map[string][]int{
		"asc":  {0, 1, 2, 3, 4, 5, 6, 7, 8},
		"desc": {8, 7, 6, 5, 4, 3, 2, 1, 0},
		"mix":  {4, 8, 1, 6, 3, 0, 7, 2, 5},
	} {
		for _, snapFirst := range []bool{true, false} {
			s := openTest(t)
			if snapFirst {
				must(t, s.UpsertConversation(snapshot))
			}
			for _, i := range order {
				must(t, noErr(s.Apply(evs[i])))
			}
			if !snapFirst {
				must(t, s.UpsertConversation(snapshot))
			}
			c := convs(t, s)[0]
			if c.Name != "final" || !reflect.DeepEqual(c.Members, []string{"ann", "bob", "cat"}) {
				t.Fatalf("%s snapFirst=%v: name %q members %v", name, snapFirst, c.Name, c.Members)
			}
		}
	}
}

func TestStaleSnapshotDoesNotRegressNameOrMembers(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "new", Members: []string{"ann", "bob"}, LastSeq: 9}))
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "old", Members: []string{"ann"}, LastSeq: 5}))
	c := convs(t, s)[0]
	if c.Name != "new" || !reflect.DeepEqual(c.Members, []string{"ann", "bob"}) || c.LastSeq != 9 {
		t.Fatalf("stale snapshot overwrote state: %+v", c.Conversation)
	}
}

func TestCursorIsTopOfContiguousRun(t *testing.T) {
	s := openTest(t)
	if s.Cursor("c1") != 0 || s.OldestSeq("c1") != 0 {
		t.Fatal("empty conversation must report 0/0")
	}
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "a"), member("c1", 2, model.MemberJoined, "bob"), posted("c1", 3, "m2", "ann", "b"))))
	if got := s.Cursor("c1"); got != 3 {
		t.Fatalf("Cursor = %d, want 3", got)
	}
	// A live event past a gap is held but does not advance the cursor.
	must(t, noErr(s.Apply(posted("c1", 6, "m3", "ann", "c"))))
	if got := s.Cursor("c1"); got != 3 {
		t.Fatalf("Cursor with gap = %d, want 3", got)
	}
	if got := msgs(t, s, "c1"); len(got) != 3 {
		t.Fatalf("event past the gap not stored: %d messages", len(got))
	}
	must(t, noErr(s.Apply(posted("c1", 4, "m4", "ann", "d"))))
	if got := s.Cursor("c1"); got != 4 {
		t.Fatalf("Cursor = %d, want 4", got)
	}
	must(t, noErr(s.Apply(posted("c1", 5, "m5", "ann", "e"))))
	if got := s.Cursor("c1"); got != 6 {
		t.Fatalf("Cursor after filling the hole = %d, want 6", got)
	}
}

func TestOldestSeqAndBackfill(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(posted("c1", 10, "m10", "ann", "a"), posted("c1", 11, "m11", "ann", "b"))))
	if s.OldestSeq("c1") != 10 || s.Cursor("c1") != 11 {
		t.Fatalf("oldest %d cursor %d, want 10 / 11", s.OldestSeq("c1"), s.Cursor("c1"))
	}
	// A history page arrives after newer events are stored.
	must(t, noErr(s.Apply(posted("c1", 8, "m8", "ann", "x"), posted("c1", 9, "m9", "ann", "y"))))
	if s.OldestSeq("c1") != 8 || s.Cursor("c1") != 11 {
		t.Fatalf("after backfill oldest %d cursor %d, want 8 / 11", s.OldestSeq("c1"), s.Cursor("c1"))
	}
	if s.Cursor("other") != 0 {
		t.Fatal("cursor leaked across conversations")
	}
}
