package store

import (
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

// lookupFixture holds conversations covering each part of a view: renames
// and membership events past the snapshot, unread counts, a deleted newest
// message, no messages at all, and one the user has left.
func lookupFixture(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	must(t, s.UpsertPeople([]model.Person{{Handle: "me", DisplayName: "Me Myself", Agent: true}}))
	for _, c := range []model.Conversation{
		{ID: "r1", Kind: model.ConvRoom, Name: "eng", Members: []string{"me", "ann"}, LastSeq: 2, ReadSeq: 1},
		{ID: "r2", Kind: model.ConvRoom, Name: "ops", Members: []string{"me"}, LastSeq: 0},
		{ID: "d1", Kind: model.ConvDM, Members: []string{"me", "bob"}, LastSeq: 1},
		{ID: "gone", Kind: model.ConvRoom, Name: "old", Members: []string{"me"}, LastSeq: 1},
	} {
		must(t, s.UpsertConversation(c))
	}
	must(t, noErr(s.Apply(
		posted("r1", 1, "m1", "ann", "one"),
		posted("r1", 2, "m2", "me", "two"),
		posted("r1", 3, "m3", "ann", "three"),
		edited("r1", 4, "m1", "ann", "one, edited"),
		renamed("r1", 5, "engineering"),
		member("r1", 6, model.MemberJoined, "cat"),
		posted("d1", 1, "m1", "bob", "hi"),
		deleted("d1", 2, "m1", "bob"),
		posted("gone", 1, "m1", "ann", "bye"),
	)))
	must(t, s.MarkGone("gone"))
	return s
}

func TestConvViewMatchesConversations(t *testing.T) {
	s := lookupFixture(t)
	all := convs(t, s)
	if len(all) != 3 {
		t.Fatalf("Conversations = %v, want three", ids(all))
	}
	for _, want := range all {
		got, err := s.ConvView(want.ID)
		must(t, err)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ConvView(%s) = %+v, want %+v", want.ID, got, want)
		}
	}
	for _, id := range []string{"gone", "missing"} {
		if _, err := s.ConvView(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ConvView(%s) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestConvIDsMatchConversations(t *testing.T) {
	s := lookupFixture(t)
	got, err := s.ConvIDs()
	must(t, err)
	want := ids(convs(t, s))
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) || len(got) != 3 {
		t.Fatalf("ConvIDs = %v, want %v", got, want)
	}
}

func TestOutboxItemMatchesOutbox(t *testing.T) {
	s := lookupFixture(t)
	must(t, s.Enqueue(OutboxItem{MsgID: "q1", ConvID: "r1", Text: "hello @ann", Mentions: []string{"ann"}, Source: "cli"}))
	enqueue(t, s, "gone", "q2", "into a conversation the user left")
	must(t, s.MarkFailed("q2", "refused"))
	for _, conv := range []string{"r1", "gone"} {
		items, err := s.Outbox(conv)
		must(t, err)
		for _, want := range items {
			got, err := s.OutboxItem(want.MsgID)
			must(t, err)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("OutboxItem(%s) = %+v, want %+v", want.MsgID, got, want)
			}
		}
	}
	if _, err := s.OutboxItem("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OutboxItem(nope) error = %v, want ErrNotFound", err)
	}
}

func TestOutboxMessagesCarryTheUsersIdentity(t *testing.T) {
	s := lookupFixture(t)
	enqueue(t, s, "r2", "q1", "first")
	enqueue(t, s, "r2", "q2", "second")
	got := msgs(t, s, "r2")
	if len(got) != 2 {
		t.Fatalf("Messages = %+v", got)
	}
	for _, m := range got {
		if m.Author != "me" || m.AuthorName != "Me Myself" || !m.Agent {
			t.Fatalf("outbox message = %+v, want the user's identity", m)
		}
	}
	one, err := s.ByMsgID("r2", "q2")
	must(t, err)
	if !reflect.DeepEqual(one, got[1]) {
		t.Fatalf("ByMsgID = %+v, want %+v", one, got[1])
	}
}

// queryPlan returns the EXPLAIN QUERY PLAN details of query, one per line.
func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	must(t, err)
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		must(t, rows.Scan(&id, &parent, &unused, &detail))
		lines = append(lines, detail)
	}
	must(t, rows.Err())
	return strings.Join(lines, "\n")
}

func TestViewQueriesUseTheirIndexes(t *testing.T) {
	s := lookupFixture(t)
	for _, tc := range []struct {
		name, query, index string
		args               []any
	}{
		{"unread", unreadSQL, "COVERING INDEX messages_unread", []any{"r1", 1, "me"}},
		{"last activity", lastActivitySQL, "events_kind_at",
			[]any{"r1", string(model.MessagePosted), string(model.MessageEdited), string(model.MessageDeleted)}},
		{"oldest", oldestSQL, "", []any{"r1"}},
	} {
		plan := queryPlan(t, s, tc.query, tc.args...)
		if tc.index != "" && !strings.Contains(plan, tc.index) {
			t.Errorf("%s plan does not use %s:\n%s", tc.name, tc.index, plan)
		}
		for _, line := range strings.Split(plan, "\n") {
			if strings.HasPrefix(line, "SCAN events") || strings.HasPrefix(line, "SCAN messages") || strings.HasPrefix(line, "SCAN skipped") {
				t.Errorf("%s plan scans a table:\n%s", tc.name, plan)
			}
		}
	}
	if plan := queryPlan(t, s, lastActivitySQL, "r1", "a", "b", "c"); strings.Count(plan, "SEARCH events USING COVERING INDEX events_kind_at") != 3 {
		t.Errorf("last activity plan does not seek once per kind:\n%s", plan)
	}
}

// TestMigrationFromVersionTwo opens a database left at schema version 2 with
// data in it, and checks the upgraded store answers as before.
func TestMigrationFromVersionTwo(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "acct")
	must(t, err)
	must(t, s.SetMe("me"))
	must(t, s.UpsertConversation(model.Conversation{ID: "r1", Kind: model.ConvRoom, Name: "eng", Members: []string{"me"}, LastSeq: 2}))
	must(t, noErr(s.Apply(posted("r1", 1, "m1", "ann", "one"), posted("r1", 2, "m2", "ann", "two"))))
	before := convs(t, s)
	for _, stmt := range []string{`DROP INDEX events_kind_at`, `DROP INDEX messages_unread`, `PRAGMA user_version = 2`} {
		_, err := s.db.Exec(stmt)
		must(t, err)
	}
	must(t, s.Close())

	s, err = Open(dir, "acct")
	must(t, err)
	defer func() { _ = s.Close() }()
	var version int
	must(t, s.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	var n int
	err = s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('events_kind_at', 'messages_unread')`).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("indexes after migration = %d, want 2", n)
	}
	if after := convs(t, s); !reflect.DeepEqual(after, before) {
		t.Fatalf("Conversations after migration = %+v, want %+v", after, before)
	}
}
