package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const outboxCols = `msg_id, conv, text, mentions, source, created_at, state, err`

func scanOutbox(sc interface{ Scan(...any) error }) (OutboxItem, error) {
	var it OutboxItem
	var mentions string
	var created int64
	if err := sc.Scan(&it.MsgID, &it.ConvID, &it.Text, &mentions, &it.Source, &created, &it.State, &it.Err); err != nil {
		return OutboxItem{}, err
	}
	it.Mentions = splitHandles(mentions)
	it.CreatedAt = fromMS(created)
	return it, nil
}

func (s *Store) queryOutbox(where string, args ...any) ([]OutboxItem, error) {
	rows, err := s.db.Query(`SELECT `+outboxCols+` FROM outbox `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxItem
	for rows.Next() {
		it, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Enqueue stores a message to be sent, in state "sending". MsgID must be set
// and not already queued; a zero CreatedAt becomes the store clock's time.
// The row is durable once Enqueue returns.
func (s *Store) Enqueue(it OutboxItem) error {
	if it.MsgID == "" || it.ConvID == "" {
		return fmt.Errorf("%w: outbox item needs a message id and a conversation", ErrInvalid)
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = s.now()
	}
	_, err := s.db.Exec(`INSERT INTO outbox (msg_id, conv, text, mentions, source, created_at, state)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		it.MsgID, it.ConvID, it.Text, joinHandles(it.Mentions), it.Source, toMS(it.CreatedAt), StatusSending)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%w: outbox message %s", ErrExists, it.MsgID)
	}
	return err
}

// Pending returns the items still to be sent, oldest first; insertion order
// breaks ties.
func (s *Store) Pending() ([]OutboxItem, error) {
	return s.queryOutbox(`WHERE state = ?`, StatusSending)
}

// Outbox returns every item of conv, sending or failed, oldest first.
func (s *Store) Outbox(conv string) ([]OutboxItem, error) {
	return s.queryOutbox(`WHERE conv = ?`, conv)
}

// MarkFailed records that sending msgID gave up, keeping the item for retry or
// discard.
func (s *Store) MarkFailed(msgID, reason string) error {
	res, err := s.db.Exec(`UPDATE outbox SET state = ?, err = ? WHERE msg_id = ?`, StatusFailed, reason, msgID)
	if err != nil {
		return err
	}
	return requireAffected(res, "outbox message "+msgID)
}

// Requeue returns a failed item to "sending" with its creation time reset, so
// it sends after items queued meanwhile. An item already sending is untouched.
func (s *Store) Requeue(msgID string) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE outbox SET state = ?, err = '', created_at = ?
			WHERE msg_id = ? AND state = ?`, StatusSending, toMS(s.now()), msgID, StatusFailed)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n > 0 {
			return err
		}
		var one int
		err = tx.QueryRow(`SELECT 1 FROM outbox WHERE msg_id = ?`, msgID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: outbox message %s", ErrNotFound, msgID)
		}
		return err
	})
}

// Discard removes an item without sending it.
func (s *Store) Discard(msgID string) error {
	res, err := s.db.Exec(`DELETE FROM outbox WHERE msg_id = ?`, msgID)
	if err != nil {
		return err
	}
	return requireAffected(res, "outbox message "+msgID)
}

// outboxMessage shows an outbox item as a message from the user.
func (s *Store) outboxMessage(it OutboxItem) Message {
	me := s.Me()
	m := Message{
		ConvID: it.ConvID, MsgID: it.MsgID, Author: me, AuthorName: me,
		At: it.CreatedAt, Text: it.Text, Mentions: it.Mentions, Status: it.State,
	}
	var name string
	var agent int
	if err := s.db.QueryRow(`SELECT display_name, agent FROM people WHERE handle = ?`, me).Scan(&name, &agent); err == nil {
		m.Agent = agent != 0
		if name != "" {
			m.AuthorName = name
		}
	}
	return m
}
