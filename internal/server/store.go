// Package server holds the self-hosted chat server's persistence layer.
package server

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; the build must stay cgo-free

	"github.com/jpe0824/termchat/internal/model"
)

// Sentinel errors. Methods may wrap them; test with errors.Is.
var (
	ErrExists        = errors.New("already exists")
	ErrNotFound      = errors.New("not found")
	ErrInvalidName   = errors.New("invalid name")
	ErrForbidden     = errors.New("forbidden")
	ErrInviteInvalid = errors.New("invite is invalid, used or expired")
)

// Store is the server's SQLite database. It is safe for concurrent use, and
// several processes may open the same file at once.
type Store struct {
	db *sql.DB
	// now is the clock for every stored timestamp; tests replace it.
	now func() time.Time
}

// OpenStore opens or creates dir/server.db, applying pending migrations.
//
// Every transaction begins IMMEDIATE so that the read of a conversation's
// highest Seq and the insert that follows it cannot interleave with another
// writer, in this process or another. busy_timeout makes contending writers
// wait instead of failing.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "server.db") +
		"?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)"
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

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

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
func (s *Store) nowMS() int64   { return toMS(s.now()) }
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func notFound(what string) error { return fmt.Errorf("%w: %s", ErrNotFound, what) }
func exists(q querier, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRow(query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// cleanName normalises a handle or room name and validates it.
func cleanName(s string) (string, error) {
	n := model.NormalizeName(s)
	if !model.ValidName(n) {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, s)
	}
	return n, nil
}

func requirePerson(q querier, handle string) error {
	ok, err := exists(q, `SELECT 1 FROM people WHERE handle = ? AND removed_at IS NULL`, handle)
	if err != nil {
		return err
	}
	if !ok {
		return notFound("person " + handle)
	}
	return nil
}

func requireConv(q querier, id string) error {
	ok, err := exists(q, `SELECT 1 FROM conversations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if !ok {
		return notFound("conversation " + id)
	}
	return nil
}
