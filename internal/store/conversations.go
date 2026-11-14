package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

// ConvView is a conversation as listed: the server's fields plus what the
// store derives from the events it holds. LastSeq and ReadSeq are the server's
// and the user's positions; Cursor reports how far the store itself has got.
type ConvView struct {
	model.Conversation
	// Unread counts messages after ReadSeq that are neither the user's own nor
	// deleted.
	Unread int
	// LastText is the text of the newest message; empty when that message is
	// deleted.
	LastText string
	// LastAt is the time of the newest held message event, zero when none.
	LastAt time.Time
}

// UpsertConversation records the server's view of a conversation. Name and
// Members are a snapshot as of c.LastSeq: held membership and rename events
// newer than that override them, and a snapshot older than the one already
// held is ignored for both. LastSeq and ReadSeq never move backwards. A
// conversation the user had left comes back into Conversations().
func (s *Store) UpsertConversation(c model.Conversation) error {
	if c.ID == "" {
		return fmt.Errorf("%w: conversation without id", ErrInvalid)
	}
	return s.write(func(tx *sql.Tx) error {
		var oldSnap int64
		err := tx.QueryRow(`SELECT snap_seq FROM conversations WHERE id = ?`, c.ID).Scan(&oldSnap)
		isNew := errors.Is(err, sql.ErrNoRows)
		if err != nil && !isNew {
			return err
		}
		newer := isNew || c.LastSeq >= oldSnap

		if _, err := tx.Exec(`INSERT INTO conversations (id, kind, name, open, creator, last_seq, read_seq, snap_seq, gone)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)
			ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, open = excluded.open, creator = excluded.creator,
				last_seq = MAX(last_seq, excluded.last_seq), read_seq = MAX(read_seq, excluded.read_seq),
				name = CASE WHEN ? THEN excluded.name ELSE name END,
				snap_seq = MAX(snap_seq, excluded.snap_seq), gone = 0`,
			c.ID, string(c.Kind), c.Name, boolInt(c.Open), c.Creator, c.LastSeq, c.ReadSeq, c.LastSeq, newer); err != nil {
			return err
		}
		if newer {
			if _, err := tx.Exec(`DELETE FROM conv_members WHERE conv = ?`, c.ID); err != nil {
				return err
			}
			for _, h := range c.Members {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO conv_members (conv, handle) VALUES (?, ?)`, c.ID, h); err != nil {
					return err
				}
			}
		}
		var me string
		if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'me'`).Scan(&me); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if me == "" {
			return nil
		}
		return syncGone(tx, c.ID, me)
	})
}

// MarkGone hides a conversation the user has left or been removed from. Its
// history stays stored and searchable.
func (s *Store) MarkGone(conv string) error {
	res, err := s.db.Exec(`UPDATE conversations SET gone = 1 WHERE id = ?`, conv)
	if err != nil {
		return err
	}
	return requireAffected(res, "conversation "+conv)
}

// SetRead advances the user's read position; it never moves backwards.
func (s *Store) SetRead(conv string, seq int64) error {
	res, err := s.db.Exec(`UPDATE conversations SET read_seq = MAX(read_seq, ?) WHERE id = ?`, seq, conv)
	if err != nil {
		return err
	}
	return requireAffected(res, "conversation "+conv)
}

func requireAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return nil
}

// Conversations lists the conversations the user is in, most recent activity
// first; those with no messages follow, ordered by name.
func (s *Store) Conversations() ([]ConvView, error) {
	me := s.Me()
	rows, err := s.db.Query(`SELECT id, kind, name, open, creator, last_seq, read_seq, snap_seq
		FROM conversations WHERE gone = 0`)
	if err != nil {
		return nil, err
	}
	var out []ConvView
	var snaps []int64
	for rows.Next() {
		var v ConvView
		var kind string
		var open int
		var snap int64
		if err := rows.Scan(&v.ID, &kind, &v.Name, &open, &v.Creator, &v.LastSeq, &v.ReadSeq, &snap); err != nil {
			_ = rows.Close()
			return nil, err
		}
		v.Kind, v.Open = model.ConvKind(kind), open != 0
		out = append(out, v)
		snaps = append(snaps, snap)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fillView(&out[i], snaps[i], me); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.LastAt.Equal(b.LastAt) {
			return a.LastAt.After(b.LastAt)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out, nil
}

func (s *Store) fillView(v *ConvView, snap int64, me string) error {
	// A rename newer than the snapshot wins.
	var name string
	err := s.db.QueryRow(`SELECT name FROM events WHERE conv = ? AND kind = ? AND seq > ?
		ORDER BY seq DESC LIMIT 1`, v.ID, string(model.RoomRenamed), snap).Scan(&name)
	if err == nil {
		v.Name = name
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	members, err := s.members(v.ID, snap)
	if err != nil {
		return err
	}
	v.Members = members

	if err := s.db.QueryRow(`SELECT count(*) FROM messages
		WHERE conv = ? AND seq > ? AND author <> ? AND deleted = 0`, v.ID, v.ReadSeq, me).Scan(&v.Unread); err != nil {
		return err
	}
	var text string
	var gone int
	err = s.db.QueryRow(`SELECT text, deleted FROM messages WHERE conv = ? ORDER BY seq DESC LIMIT 1`, v.ID).Scan(&text, &gone)
	if err == nil && gone == 0 {
		v.LastText = text
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var lastAt sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(at) FROM events WHERE conv = ? AND kind IN (?, ?, ?)`, v.ID,
		string(model.MessagePosted), string(model.MessageEdited), string(model.MessageDeleted)).Scan(&lastAt); err != nil {
		return err
	}
	if lastAt.Valid {
		v.LastAt = fromMS(lastAt.Int64)
	}
	return nil
}

// members is the snapshot's member list with every held membership event newer
// than the snapshot replayed in Seq order.
func (s *Store) members(conv string, snap int64) ([]string, error) {
	set := map[string]bool{}
	rows, err := s.db.Query(`SELECT handle FROM conv_members WHERE conv = ?`, conv)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			_ = rows.Close()
			return nil, err
		}
		set[h] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT kind, target FROM events WHERE conv = ? AND seq > ? AND kind IN (?, ?)
		ORDER BY seq`, conv, snap, string(model.MemberJoined), string(model.MemberLeft))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, target string
		if err := rows.Scan(&kind, &target); err != nil {
			_ = rows.Close()
			return nil, err
		}
		set[target] = kind == string(model.MemberJoined)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []string
	for h, in := range set {
		if in {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out, nil
}

// OldestSeq is the lowest Seq held for conv, 0 when none.
func (s *Store) OldestSeq(conv string) int64 {
	var n sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(seq) FROM events WHERE conv = ?`, conv).Scan(&n); err != nil {
		return 0
	}
	return n.Int64
}

// Cursor is the top of the contiguous run of held events that starts at
// OldestSeq: the largest Seq such that every Seq from OldestSeq up to it is
// held. An event past a gap is stored but does not advance it, so a sync from
// Cursor fills the hole.
func (s *Store) Cursor(conv string) int64 {
	oldest := s.OldestSeq(conv)
	if oldest == 0 {
		return 0
	}
	var top int64
	err := s.db.QueryRow(`SELECT MIN(e.seq) FROM events e WHERE e.conv = ? AND e.seq >= ?
		AND NOT EXISTS (SELECT 1 FROM events n WHERE n.conv = e.conv AND n.seq = e.seq + 1)`, conv, oldest).Scan(&top)
	if err != nil {
		return 0
	}
	return top
}
