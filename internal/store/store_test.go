package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// openTest opens a store for "me" in a temporary directory with a clock that
// advances one second per reading.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tick := t0
	s.now = func() time.Time { tick = tick.Add(time.Second); return tick }
	if err := s.SetMe("me"); err != nil {
		t.Fatal(err)
	}
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func txt(s string) *model.Body { return &model.Body{Format: "text/plain", Data: []byte(s)} }

// evAt is the timestamp used for the event with the given Seq.
func evAt(seq int64) time.Time { return t0.Add(time.Duration(seq) * time.Minute) }

func posted(conv string, seq int64, msgID, actor, text string) model.Event {
	ev := model.Event{ConvID: conv, Seq: seq, Kind: model.MessagePosted, Actor: actor, At: evAt(seq), MsgID: msgID}
	if text != "" {
		ev.Body = txt(text)
	}
	return ev
}

func edited(conv string, seq int64, msgID, actor, text string) model.Event {
	ev := posted(conv, seq, msgID, actor, text)
	ev.Kind = model.MessageEdited
	return ev
}

func deleted(conv string, seq int64, msgID, actor string) model.Event {
	return model.Event{ConvID: conv, Seq: seq, Kind: model.MessageDeleted, Actor: actor, At: evAt(seq), MsgID: msgID}
}

func member(conv string, seq int64, kind model.EventKind, target string) model.Event {
	return model.Event{ConvID: conv, Seq: seq, Kind: kind, Actor: "x", At: evAt(seq), Target: target}
}

func renamed(conv string, seq int64, name string) model.Event {
	return model.Event{ConvID: conv, Seq: seq, Kind: model.RoomRenamed, Actor: "x", At: evAt(seq), Name: name}
}

func texts(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// tablesHolding returns the tables (shadow tables of the full-text index
// included) with any cell containing needle.
func tablesHolding(t *testing.T, s *Store, needle string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	must(t, err)
	var names []string
	for rows.Next() {
		var n string
		must(t, rows.Scan(&n))
		names = append(names, n)
	}
	must(t, rows.Err())
	must(t, rows.Close())

	var found []string
	for _, n := range names {
		r, err := s.db.Query(`SELECT * FROM "` + n + `"`)
		must(t, err)
		cols, _ := r.Columns()
		hit := false
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			must(t, r.Scan(ptrs...))
			for _, v := range vals {
				switch x := v.(type) {
				case string:
					hit = hit || strings.Contains(x, needle)
				case []byte:
					hit = hit || strings.Contains(string(x), needle)
				}
			}
		}
		must(t, r.Err())
		must(t, r.Close())
		if hit {
			found = append(found, n)
		}
	}
	return found
}

func TestFTS5Available(t *testing.T) {
	s := openTest(t)
	_, err := s.db.Exec(`CREATE VIRTUAL TABLE temp.probe USING fts5(x)`)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO probe(x) VALUES ('hello world')`)
	must(t, err)
	var n int
	must(t, s.db.QueryRow(`SELECT count(*) FROM probe WHERE probe MATCH 'world'`).Scan(&n))
	if n != 1 {
		t.Fatalf("fts5 match count = %d, want 1", n)
	}
}

func TestOpenUsesAccountPathAndModes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "alice")
	must(t, err)
	defer func() { _ = s.Close() }()
	path := filepath.Join(dir, "accounts", "alice", "client.db")
	fi, err := os.Stat(path)
	must(t, err)
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("db mode = %v, want 0600", fi.Mode().Perm())
		}
		di, err := os.Stat(filepath.Dir(path))
		must(t, err)
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
		}
	}
	var mode string
	must(t, s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	must(t, s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpenRejectsBadAccount(t *testing.T) {
	for _, acct := range []string{"", "../x", "a/b", `a\b`, "."} {
		if _, err := Open(t.TempDir(), acct); !errors.Is(err, ErrInvalid) {
			t.Errorf("Open(%q) error = %v, want ErrInvalid", acct, err)
		}
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "acct")
	must(t, err)
	_, err = s.db.Exec(`PRAGMA user_version = 99`)
	must(t, err)
	must(t, s.Close())
	_, err = Open(dir, "acct")
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("Open error = %v, want a newer-schema refusal", err)
	}
}

func TestCloseTwice(t *testing.T) {
	s, err := Open(t.TempDir(), "acct")
	must(t, err)
	must(t, s.Close())
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

func TestMeSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "acct")
	must(t, err)
	if got := s.Me(); got != "" {
		t.Fatalf("Me before SetMe = %q, want empty", got)
	}
	must(t, s.SetMe("jason"))
	must(t, s.Close())
	s, err = Open(dir, "acct")
	must(t, err)
	defer func() { _ = s.Close() }()
	if got := s.Me(); got != "jason" {
		t.Fatalf("Me after reopen = %q, want jason", got)
	}
}

func TestPeopleUpsertAndList(t *testing.T) {
	s := openTest(t)
	must(t, s.UpsertPeople([]model.Person{
		{Handle: "zed", DisplayName: "Zed"},
		{Handle: "bot", DisplayName: "Bot", Agent: true, Owner: "zed"},
	}))
	must(t, s.UpsertPeople([]model.Person{{Handle: "zed", DisplayName: "Zed Z", Admin: true}}))
	got, err := s.People()
	must(t, err)
	want := []model.Person{
		{Handle: "bot", DisplayName: "Bot", Agent: true, Owner: "zed"},
		{Handle: "zed", DisplayName: "Zed Z", Admin: true},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("People = %+v, want %+v", got, want)
	}
}
