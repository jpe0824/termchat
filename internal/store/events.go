package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/jpe0824/termchat/internal/model"
)

const formatText = "text/plain"

func isMessageKind(k model.EventKind) bool {
	return k == model.MessagePosted || k == model.MessageEdited || k == model.MessageDeleted
}

type msgKey struct{ conv, msgID string }

// Apply stores events and re-derives the messages they affect, in one
// transaction. It may be called with events in any order and any number of
// times: a message's state depends only on the set of events held for it, and
// an event already held changes nothing, except that a copy arriving without a
// body (a retention purge) blanks the stored one.
//
// Message text lives in a single place per message. Once a later event holds a
// body, or the message is deleted, the bodies of the superseded events are
// cleared here, so old text never outlives its replacement in any table.
func (s *Store) Apply(evs ...model.Event) error {
	return s.write(func(tx *sql.Tx) error {
		var me string
		if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'me'`).Scan(&me); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var touched []msgKey
		seen := map[msgKey]bool{}
		touch := func(conv, msgID string) {
			k := msgKey{conv, msgID}
			if !seen[k] {
				seen[k] = true
				touched = append(touched, k)
			}
		}
		meConvs := map[string]bool{}

		for _, ev := range evs {
			if ev.ConvID == "" || ev.Seq < 1 {
				return fmt.Errorf("%w: event needs a conversation and Seq >= 1", ErrInvalid)
			}
			if isMessageKind(ev.Kind) && ev.MsgID == "" {
				return fmt.Errorf("%w: %s event %s/%d has no message id", ErrInvalid, ev.Kind, ev.ConvID, ev.Seq)
			}
			inserted, err := insertEvent(tx, ev)
			if err != nil {
				return err
			}
			if ev.Kind == model.MessagePosted && me != "" && ev.Actor == me {
				// The server confirms a send by echoing our posted event, whichever
				// state the outbox row reached. MsgIDs are unique only within a
				// conversation and the server rejects a duplicate sent by someone
				// else, so a match on anything less must keep the unsent item.
				if _, err := tx.Exec(`DELETE FROM outbox WHERE conv = ? AND msg_id = ?`, ev.ConvID, ev.MsgID); err != nil {
					return err
				}
			}
			if isMessageKind(ev.Kind) {
				if inserted {
					touch(ev.ConvID, ev.MsgID)
				} else if ev.Body == nil {
					blanked, err := blankStored(tx, ev)
					if err != nil {
						return err
					}
					if blanked {
						touch(ev.ConvID, ev.MsgID)
					}
				}
			}
			if me != "" && ev.Target == me && (ev.Kind == model.MemberJoined || ev.Kind == model.MemberLeft) {
				meConvs[ev.ConvID] = true
			}
		}

		purged := false
		for _, k := range touched {
			p, err := refreshMessage(tx, k.conv, k.msgID)
			if err != nil {
				return err
			}
			purged = purged || p
		}
		if purged {
			// Deleting from an FTS5 index leaves the old terms in its segments
			// until they are merged; rewriting the index drops them now.
			if _, err := tx.Exec(`INSERT INTO msg_fts(msg_fts) VALUES ('optimize')`); err != nil {
				return err
			}
		}
		for conv := range meConvs {
			if err := syncGone(tx, conv, me); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertEvent(tx *sql.Tx, ev model.Event) (bool, error) {
	var format sql.NullString
	var data []byte
	if ev.Body != nil {
		format = sql.NullString{String: ev.Body.Format, Valid: true}
		data = ev.Body.Data
		if data == nil {
			data = []byte{}
		}
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO events
		(conv, seq, kind, actor, at, msg_id, body_format, body_data, mentions, target, name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ConvID, ev.Seq, string(ev.Kind), ev.Actor, toMS(ev.At), ev.MsgID,
		format, data, joinHandles(ev.Mentions), ev.Target, ev.Name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// blankStored clears the body of an event already held. It reports whether
// there was a body to clear.
func blankStored(tx *sql.Tx, ev model.Event) (bool, error) {
	res, err := tx.Exec(`UPDATE events SET body_format = NULL, body_data = NULL
		WHERE conv = ? AND seq = ? AND body_format IS NOT NULL`, ev.ConvID, ev.Seq)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

type heldEvent struct {
	seq      int64
	kind     model.EventKind
	actor    string
	at       int64
	format   sql.NullString
	data     []byte
	mentions string
}

func loadMessageEvents(tx *sql.Tx, conv, msgID string) ([]heldEvent, error) {
	rows, err := tx.Query(`SELECT seq, kind, actor, at, body_format, body_data, mentions
		FROM events WHERE conv = ? AND msg_id = ? ORDER BY seq`, conv, msgID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var held []heldEvent
	for rows.Next() {
		var e heldEvent
		var kind string
		if err := rows.Scan(&e.seq, &kind, &e.actor, &e.at, &e.format, &e.data, &e.mentions); err != nil {
			return nil, err
		}
		e.kind = model.EventKind(kind)
		held = append(held, e)
	}
	return held, rows.Err()
}

// refreshMessage rebuilds the messages row and index entry for one message
// from the events held for it. It reports whether previously indexed text was
// removed or replaced.
func refreshMessage(tx *sql.Tx, conv, msgID string) (purged bool, err error) {
	held, err := loadMessageEvents(tx, conv, msgID)
	if err != nil {
		return false, err
	}
	var (
		posted, best *heldEvent
		isDeleted    bool
		isEdited     bool
	)
	for i := range held {
		e := &held[i]
		switch e.kind {
		case model.MessagePosted:
			posted = e
		case model.MessageEdited:
			isEdited = true
		case model.MessageDeleted:
			isDeleted = true
		}
		if e.format.Valid && (e.kind == model.MessagePosted || e.kind == model.MessageEdited) {
			best = e // ascending order: the highest Seq with a body wins
		}
	}

	var id int64
	var oldIndexed bool
	var oldText string
	err = tx.QueryRow(`SELECT m.id, m.text, EXISTS (SELECT 1 FROM msg_fts WHERE rowid = m.id)
		FROM messages m WHERE m.conv = ? AND m.msg_id = ?`, conv, msgID).Scan(&id, &oldText, &oldIndexed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	if posted == nil {
		if id == 0 {
			return false, nil
		}
		if oldIndexed {
			if _, err := tx.Exec(`DELETE FROM msg_fts WHERE rowid = ?`, id); err != nil {
				return false, err
			}
		}
		_, err := tx.Exec(`DELETE FROM messages WHERE id = ?`, id)
		return oldIndexed, err
	}

	// Superseded and deleted bodies are cleared from the stored events.
	keep := int64(0)
	if best != nil && !isDeleted {
		keep = best.seq
	}
	if _, err := tx.Exec(`UPDATE events SET body_format = NULL, body_data = NULL
		WHERE conv = ? AND msg_id = ? AND body_format IS NOT NULL AND seq <> ?`, conv, msgID, keep); err != nil {
		return false, err
	}

	var text, mentions string
	indexed := false
	gone := isDeleted || best == nil
	if !gone {
		mentions = best.mentions
		if best.format.String == formatText {
			text, indexed = string(best.data), true
		} else {
			text = UnsupportedText
		}
	}

	if id == 0 {
		res, err := tx.Exec(`INSERT INTO messages (conv, msg_id, seq, at, author, text, mentions, edited, deleted)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			conv, msgID, posted.seq, posted.at, posted.actor, text, mentions, boolInt(isEdited), boolInt(gone))
		if err != nil {
			return false, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return false, err
		}
	} else if _, err := tx.Exec(`UPDATE messages SET seq = ?, at = ?, author = ?, text = ?, mentions = ?, edited = ?, deleted = ?
		WHERE id = ?`, posted.seq, posted.at, posted.actor, text, mentions, boolInt(isEdited), boolInt(gone), id); err != nil {
		return false, err
	}

	if oldIndexed && !(indexed && oldText == text) {
		if _, err := tx.Exec(`DELETE FROM msg_fts WHERE rowid = ?`, id); err != nil {
			return false, err
		}
		purged = true
		oldIndexed = false
	}
	if indexed && !oldIndexed {
		if _, err := tx.Exec(`INSERT INTO msg_fts (rowid, text) VALUES (?, ?)`, id, text); err != nil {
			return false, err
		}
	}
	return purged, nil
}

// syncGone marks a conversation gone, or clears the mark, according to the
// newest membership event about the user that is newer than the snapshot.
func syncGone(tx *sql.Tx, conv, me string) error {
	var snap int64
	if err := tx.QueryRow(`SELECT snap_seq FROM conversations WHERE id = ?`, conv).Scan(&snap); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var kind string
	err := tx.QueryRow(`SELECT kind FROM events WHERE conv = ? AND target = ? AND seq > ?
		AND kind IN (?, ?) ORDER BY seq DESC LIMIT 1`,
		conv, me, snap, string(model.MemberJoined), string(model.MemberLeft)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE conversations SET gone = ? WHERE id = ?`,
		boolInt(kind == string(model.MemberLeft)), conv)
	return err
}
