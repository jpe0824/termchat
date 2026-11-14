package store

import (
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

func TestSearchFindsAcrossConversations(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(
		posted("c1", 1, "m1", "ann", "deploy the quokka service"),
		posted("c2", 1, "m2", "bob", "quokka dashboard is down"),
		posted("c2", 2, "m3", "bob", "unrelated chatter"),
	))
	got, err := s.Search("quokka", 10)
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("Search = %+v, want 2 results", got)
	}
	convs := map[string]bool{got[0].ConvID: true, got[1].ConvID: true}
	if !convs["c1"] || !convs["c2"] {
		t.Fatalf("results from %v, want c1 and c2", convs)
	}
}

func TestSearchNewestFirstAndLimit(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(
		posted("c1", 1, "m1", "ann", "tapir one"),
		posted("c2", 5, "m2", "ann", "tapir two"),
		posted("c1", 3, "m3", "ann", "tapir three"),
	))
	got, err := s.Search("tapir", 0)
	must(t, err)
	if want := []string{"tapir two", "tapir three", "tapir one"}; len(got) != 3 || got[0].Text != want[0] || got[1].Text != want[1] || got[2].Text != want[2] {
		t.Fatalf("order = %v, want %v", texts(got), want)
	}
	got, err = s.Search("tapir", 2)
	must(t, err)
	if len(got) != 2 || got[0].Text != "tapir two" {
		t.Fatalf("limited = %v", texts(got))
	}
}

func TestSearchTreatsQueryAsPlainWords(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(
		posted("c1", 1, "m1", "ann", "cats AND dogs"),
		posted("c1", 2, "m2", "ann", "just cats"),
		posted("c1", 3, "m3", "ann", "NEAR the end"),
		posted("c1", 4, "m4", "ann", "star* wars"),
	))
	cases := []struct {
		q    string
		want int
	}{
		{`cats AND dogs`, 1}, // AND is a word here: only m1 holds all three
		{`cats dogs`, 1},
		{`cats`, 2},
		{`"`, 0},
		{`""`, 0},
		{`*`, 0},
		{`-`, 0},
		{`AND`, 1},
		{`NEAR(`, 1}, // plain word near
		{`NEAR`, 1},
		{`star*`, 1},
		{`-cats`, 2},
		{`cats OR bogus`, 0},
		{`co:l`, 0},
		{`(cats`, 2},
		{`^cats`, 2},
		{``, 0},
		{"  \t\n ", 0},
	}
	for _, c := range cases {
		got, err := s.Search(c.q, 10)
		if err != nil {
			t.Errorf("Search(%q) error: %v", c.q, err)
			continue
		}
		if len(got) != c.want {
			t.Errorf("Search(%q) = %v, want %d results", c.q, texts(got), c.want)
		}
	}
}

func TestSearchCoversConversationsMarkedGone(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "old"}))
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "capybara archive")))
	must(t, s.MarkGone("c1"))
	got, err := s.Search("capybara", 10)
	must(t, err)
	if len(got) != 1 || got[0].ConvID != "c1" {
		t.Fatalf("Search = %+v", got)
	}
}

func TestSearchIsCaseInsensitiveAndMatchesWholeWords(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "Gopher Time")))
	for _, q := range []string{"gopher", "GOPHER", "time gopher"} {
		if got, err := s.Search(q, 10); err != nil || len(got) != 1 {
			t.Errorf("Search(%q) = %v, %v, want 1 result", q, texts(got), err)
		}
	}
}
