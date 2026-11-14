// Package store is the client's local SQLite database: a copy of the
// conversations and events the user can see, read positions, the outbox of
// messages waiting to be sent, and a full-text index over message text.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; the build must stay cgo-free

	"github.com/jpe0824/termchat/internal/model"
)

// Sentinel errors. Methods may wrap them; test with errors.Is.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrInvalid  = errors.New("invalid argument")
)

// Message statuses for outbox entries shown among stored messages.
const (
	StatusSending = "sending"
	StatusFailed  = "failed"
)

// UnsupportedText is shown for a body whose format the client cannot decode.
const UnsupportedText = "[unsupported message]"

// Store is the client database. The background process is its only writer.
type Store struct {
	db *sql.DB
	// now is the clock for stored timestamps; tests replace it.
	now func() time.Time
}

// Message is one message as displayed. Stored messages have Status "";
// outbox entries have "sending" or "failed" and Seq 0.
type Message struct {
	ConvID, MsgID, Author string
	Seq                   int64
	At                    time.Time
	Text                  string
	Mentions              []string
	Edited, Deleted       bool
	Agent                 bool
	Status                string
	// AuthorName is the author's display name, or the handle when the author
	// has none or is unknown.
	AuthorName string
}

// OutboxItem is a message waiting to be sent.
type OutboxItem struct {
	MsgID, ConvID, Text string
	Mentions            []string
	Source              string // "tui" or "cli"
	CreatedAt           time.Time
	State               string // "sending" or "failed"
	Err                 string
}

// Open opens or creates dataDir/accounts/<account>/client.db and applies
// pending migrations.
//
// Every transaction begins IMMEDIATE so a read-then-write cannot interleave
// with another writer; busy_timeout makes contending writers wait. secure_delete
// zeroes freed pages so purged message text does not linger in the file.
func Open(dataDir, account string) (*Store, error) {
	if account == "" || account == "." || account == ".." || strings.ContainsAny(account, `/\`) {
		return nil, fmt.Errorf("%w: account name %q", ErrInvalid, account)
	}
	dir := filepath.Join(dataDir, "accounts", account)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "client.db")
	// Create the file ourselves so it is private from its first byte.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	dsn := path +
		"?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=secure_delete(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close releases the database. It is safe to call more than once.
func (s *Store) Close() error { return s.db.Close() }

// write runs fn in an immediate transaction and commits if it returns nil.
func (s *Store) write(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Timestamps are stored as UTC Unix milliseconds.
func toMS(t time.Time) int64    { return t.UTC().UnixMilli() }
func fromMS(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Handles are stored comma-joined; they never contain commas.
func joinHandles(hs []string) string { return strings.Join(hs, ",") }
func splitHandles(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// SetMe records the signed-in user's handle, used for unread counts and for
// attributing outbox messages.
func (s *Store) SetMe(handle string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES ('me', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, handle)
	return err
}

// Me returns the handle recorded by SetMe, or "" if none.
func (s *Store) Me() string {
	var me string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'me'`).Scan(&me); err != nil {
		return ""
	}
	return me
}

// UpsertPeople inserts or updates identities; they are the source of
// Message.Agent and display names.
func (s *Store) UpsertPeople(ps []model.Person) error {
	return s.write(func(tx *sql.Tx) error {
		for _, p := range ps {
			if p.Handle == "" {
				return fmt.Errorf("%w: person without handle", ErrInvalid)
			}
			if _, err := tx.Exec(`INSERT INTO people(handle, display_name, agent, admin, owner)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(handle) DO UPDATE SET display_name = excluded.display_name,
					agent = excluded.agent, admin = excluded.admin, owner = excluded.owner`,
				p.Handle, p.DisplayName, boolInt(p.Agent), boolInt(p.Admin), p.Owner); err != nil {
				return err
			}
		}
		return nil
	})
}

// People returns every known identity ordered by handle.
func (s *Store) People() ([]model.Person, error) {
	rows, err := s.db.Query(`SELECT handle, display_name, agent, admin, owner FROM people ORDER BY handle`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Person
	for rows.Next() {
		var p model.Person
		var agent, admin int
		if err := rows.Scan(&p.Handle, &p.DisplayName, &agent, &admin, &p.Owner); err != nil {
			return nil, err
		}
		p.Agent, p.Admin = agent != 0, admin != 0
		out = append(out, p)
	}
	return out, rows.Err()
}
