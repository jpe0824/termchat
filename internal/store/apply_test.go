package store

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	must(t, s.db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
	return n
}

func msgs(t *testing.T, s *Store, conv string) []Message {
	t.Helper()
	ms, err := s.Messages(conv, 0, 0)
	must(t, err)
	return ms
}

func TestApplyIsIdempotent(t *testing.T) {
	s := openTest(t)
	evs := []model.Event{
		posted("c1", 1, "m1", "ann", "hello"),
		edited("c1", 2, "m1", "ann", "hello again"),
		posted("c1", 3, "m2", "bob", "second"),
	}
	must(t, noErr(s.Apply(evs...)))
	first := msgs(t, s, "c1")
	events, messages, fts := count(t, s, "events"), count(t, s, "messages"), count(t, s, "msg_fts")

	must(t, noErr(s.Apply(evs...)))
	must(t, noErr(s.Apply(evs[1])))
	if got := msgs(t, s, "c1"); !reflect.DeepEqual(got, first) {
		t.Fatalf("Messages changed after re-apply:\n got %+v\nwant %+v", got, first)
	}
	if e, m, f := count(t, s, "events"), count(t, s, "messages"), count(t, s, "msg_fts"); e != events || m != messages || f != fts {
		t.Fatalf("row counts changed: events %d->%d messages %d->%d fts %d->%d", events, e, messages, m, fts, f)
	}
	if len(first) != 2 || first[0].Text != "hello again" || first[1].Text != "second" {
		t.Fatalf("Messages = %+v", first)
	}
}

// The server blanks earlier bodies once an edit or delete exists; live pushes
// arrive before that happens. Both shapes must converge on the same result.
func TestApplyIsIndependentOfArrivalOrder(t *testing.T) {
	shapes := map[string][]model.Event{
		"blanked": {
			posted("c1", 1, "m1", "ann", ""),
			edited("c1", 2, "m1", "ann", ""),
			posted("c1", 3, "m2", "bob", "other"),
			edited("c1", 4, "m1", "ann", "third"),
			posted("c1", 5, "m3", "ann", ""),
			deleted("c1", 6, "m3", "ann"),
			member("c1", 7, model.MemberJoined, "cat"),
		},
		"live": {
			posted("c1", 1, "m1", "ann", "first"),
			edited("c1", 2, "m1", "ann", "second"),
			posted("c1", 3, "m2", "bob", "other"),
			edited("c1", 4, "m1", "ann", "third"),
			posted("c1", 5, "m3", "ann", "gone"),
			deleted("c1", 6, "m3", "ann"),
			member("c1", 7, model.MemberJoined, "cat"),
		},
	}
	for name, evs := range shapes {
		t.Run(name, func(t *testing.T) {
			var want []Message
			orders := map[string][]int{
				"ascending":  {0, 1, 2, 3, 4, 5, 6},
				"newest":     {6, 5, 4, 3, 2, 1, 0},
				"interleave": {3, 0, 5, 1, 6, 2, 4},
			}
			rng := rand.New(rand.NewSource(7))
			for i := 0; i < 8; i++ {
				orders["shuffle"+string(rune('a'+i))] = rng.Perm(len(evs))
			}
			first := true
			for oname, order := range orders {
				s := openTest(t)
				for _, i := range order {
					must(t, noErr(s.Apply(evs[i])))
				}
				got := msgs(t, s, "c1")
				if first {
					want, first = got, false
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("order %s:\n got %+v\nwant %+v", oname, got, want)
				}
			}
			if len(want) != 3 {
				t.Fatalf("want 3 messages, got %+v", want)
			}
			if want[0].MsgID != "m1" || want[0].Text != "third" || !want[0].Edited || want[0].Seq != 1 || want[0].At != evAt(1) || want[0].Author != "ann" {
				t.Errorf("m1 = %+v", want[0])
			}
			if want[1].MsgID != "m2" || want[1].Text != "other" || want[1].Edited || want[1].Author != "bob" {
				t.Errorf("m2 = %+v", want[1])
			}
			if want[2].MsgID != "m3" || !want[2].Deleted || want[2].Text != "" {
				t.Errorf("m3 = %+v", want[2])
			}
		})
	}
}

func TestApplyBatchOrderDoesNotMatter(t *testing.T) {
	a, b := openTest(t), openTest(t)
	evs := []model.Event{
		posted("c1", 1, "m1", "ann", "one"),
		edited("c1", 2, "m1", "ann", "two"),
		posted("c1", 3, "m2", "ann", "three"),
	}
	must(t, noErr(a.Apply(evs...)))
	must(t, noErr(b.Apply(evs[2], evs[1], evs[0])))
	if !reflect.DeepEqual(msgs(t, a, "c1"), msgs(t, b, "c1")) {
		t.Fatalf("batch order changed the result")
	}
}

func TestEditBeforePostedShowsNothingUntilPosted(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(edited("c1", 2, "m1", "ann", "new text"))))
	if got := msgs(t, s, "c1"); len(got) != 0 {
		t.Fatalf("message shown without its posted event: %+v", got)
	}
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "old text"))))
	got := msgs(t, s, "c1")
	if len(got) != 1 || got[0].Text != "new text" || !got[0].Edited || got[0].Seq != 1 {
		t.Fatalf("Messages = %+v", got)
	}
	if hits := tablesHolding(t, s, "old text"); len(hits) != 0 {
		t.Fatalf("old text still held in %v", hits)
	}
}

func TestEditReplacesTextAndIndex(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "pineapple pizza"))))
	if r, err := s.Search("pineapple", 10); err != nil || len(r) != 1 {
		t.Fatalf("Search(pineapple) = %v, %v", r, err)
	}
	must(t, noErr(s.Apply(edited("c1", 2, "m1", "ann", "margherita pizza"))))

	if r, err := s.Search("pineapple", 10); err != nil || len(r) != 0 {
		t.Fatalf("Search(pineapple) after edit = %v, %v", r, err)
	}
	r, err := s.Search("margherita", 10)
	must(t, err)
	if len(r) != 1 || r[0].Text != "margherita pizza" || !r[0].Edited {
		t.Fatalf("Search(margherita) = %+v", r)
	}
	if hits := tablesHolding(t, s, "pineapple"); len(hits) != 0 {
		t.Fatalf("old text still held in %v", hits)
	}
	if got := msgs(t, s, "c1"); len(got) != 1 {
		t.Fatalf("edit produced %d entries, want 1", len(got))
	}
}

func TestDeletePurgesTextAndIndex(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(
		posted("c1", 1, "m1", "ann", "zebrafish secret"),
		edited("c1", 2, "m1", "ann", "zebrafish revised"),
	)))
	must(t, noErr(s.Apply(deleted("c1", 3, "m1", "ann"))))
	got := msgs(t, s, "c1")
	if len(got) != 1 || !got[0].Deleted || got[0].Text != "" {
		t.Fatalf("Messages = %+v", got)
	}
	if r, err := s.Search("zebrafish", 10); err != nil || len(r) != 0 {
		t.Fatalf("Search after delete = %v, %v", r, err)
	}
	if hits := tablesHolding(t, s, "zebrafish"); len(hits) != 0 {
		t.Fatalf("deleted text still held in %v", hits)
	}
}

func TestLateArrivingOldBodiesDoNotResurface(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(edited("c1", 4, "m1", "ann", "current"))))
	// A history page delivers the original with its body after the edit is held.
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "walrus original"))))
	must(t, noErr(s.Apply(posted("c1", 2, "m2", "ann", "walrus doomed"))))
	must(t, noErr(s.Apply(deleted("c1", 5, "m2", "ann"))))
	// An earlier event with a body arrives once more after the deletion.
	must(t, noErr(s.Apply(edited("c1", 3, "m2", "ann", "walrus late edit"))))
	if hits := tablesHolding(t, s, "walrus"); len(hits) != 0 {
		t.Fatalf("stale text held in %v", hits)
	}
}

func TestBlankingRemovesTextEverywhere(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "narwhal plans"))))
	// Retention purge: the same event returns with its body cleared.
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", ""))))
	if hits := tablesHolding(t, s, "narwhal"); len(hits) != 0 {
		t.Fatalf("blanked text still held in %v", hits)
	}
	if r, err := s.Search("narwhal", 10); err != nil || len(r) != 0 {
		t.Fatalf("Search after blanking = %v, %v", r, err)
	}
	got := msgs(t, s, "c1")
	if len(got) != 1 || !got[0].Deleted || got[0].Text != "" {
		t.Fatalf("blanked message = %+v, want the deleted placeholder", got)
	}
	// A later event that carries a body supplies the text.
	must(t, noErr(s.Apply(edited("c1", 2, "m1", "ann", "restored"))))
	got = msgs(t, s, "c1")
	if len(got) != 1 || got[0].Deleted || got[0].Text != "restored" || !got[0].Edited {
		t.Fatalf("after later edit = %+v", got)
	}
}

func TestStoredBlankedPostedStaysBlankedWhenBodyReappears(t *testing.T) {
	s := openTest(t)
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", ""))))
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", "owl"))))
	if got := msgs(t, s, "c1"); len(got) != 1 || got[0].Text != "" || !got[0].Deleted {
		t.Fatalf("a purged event regained its body: %+v", got)
	}
}

func TestUnsupportedFormatIsPlaceholderAndNotIndexed(t *testing.T) {
	s := openTest(t)
	ev := posted("c1", 1, "m1", "ann", "")
	ev.Body = &model.Body{Format: "image/png", Data: []byte("flamingo")}
	must(t, noErr(s.Apply(ev)))
	got := msgs(t, s, "c1")
	if len(got) != 1 || got[0].Text != UnsupportedText || got[0].Deleted {
		t.Fatalf("Messages = %+v", got)
	}
	if r, err := s.Search("flamingo", 10); err != nil || len(r) != 0 {
		t.Fatalf("Search found opaque body: %v, %v", r, err)
	}
}

func TestTextIsStoredAsReceived(t *testing.T) {
	s := openTest(t)
	raw := "  hi \x1b[31m red <b>&amp;</b>\n"
	must(t, noErr(s.Apply(posted("c1", 1, "m1", "ann", raw))))
	if got := msgs(t, s, "c1"); len(got) != 1 || got[0].Text != raw {
		t.Fatalf("Messages = %+v", got)
	}
}

func TestMentionsFollowTheCurrentBody(t *testing.T) {
	s := openTest(t)
	p := posted("c1", 1, "m1", "ann", "hi @bob")
	p.Mentions = []string{"bob"}
	e := edited("c1", 2, "m1", "ann", "hi @cat @dan")
	e.Mentions = []string{"cat", "dan"}
	must(t, noErr(s.Apply(e, p)))
	got := msgs(t, s, "c1")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Mentions, []string{"cat", "dan"}) {
		t.Fatalf("Messages = %+v", got)
	}
}

func TestApplyIsAtomic(t *testing.T) {
	s := openTest(t)
	_, err := s.Apply(posted("c1", 1, "m1", "ann", "fine"), model.Event{ConvID: "c1", Seq: 0, Kind: model.MessagePosted, MsgID: "m2"})
	if err == nil {
		t.Fatal("Apply accepted an event with Seq 0")
	}
	if n := count(t, s, "events"); n != 0 {
		t.Fatalf("%d events stored from a failed batch", n)
	}
}

func TestApplyRejectsMalformedEvents(t *testing.T) {
	s := openTest(t)
	bad := []model.Event{
		{Seq: 1, Kind: model.MessagePosted, MsgID: "m"},
		{ConvID: "c", Seq: -1, Kind: model.MessagePosted, MsgID: "m"},
		{ConvID: "c", Seq: 1, Kind: model.MessagePosted},
	}
	for i, ev := range bad {
		if _, err := s.Apply(ev); err == nil {
			t.Errorf("case %d: Apply accepted %+v", i, ev)
		}
	}
}

func TestApplyReturnsTheEventsItNewlyStored(t *testing.T) {
	s := openTest(t)
	held := posted("c1", 1, "m1", "ann", "held")
	added, err := s.Apply(held)
	must(t, err)
	if !reflect.DeepEqual(added, []model.Event{held}) {
		t.Fatalf("first Apply returned %+v, want the one event", added)
	}

	m3, m2 := posted("c1", 3, "m3", "ann", "three"), posted("c1", 2, "m2", "bob", "two")
	join := member("c1", 4, model.MemberJoined, "cat")
	added, err = s.Apply(m3, held, m2, m3, join)
	must(t, err)
	if want := []model.Event{m3, m2, join}; !reflect.DeepEqual(added, want) {
		t.Fatalf("Apply returned %+v\nwant %+v (new ones only, once each, in input order)", added, want)
	}

	added, err = s.Apply(posted("c1", 5, "m5", "ann", "fine"), model.Event{ConvID: "c1", Seq: 0, Kind: model.MessagePosted, MsgID: "bad"})
	if err == nil || added != nil {
		t.Fatalf("rejected batch returned %+v, %v; want nothing and an error", added, err)
	}
}
