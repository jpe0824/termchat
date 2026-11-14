package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

const defaultHistoryLimit = 50

const eventCols = `conv, seq, kind, actor, at, msg_id, body_format, body_data, mentions, target, name`

func scanEvent(sc scanner) (model.Event, error) {
	var ev model.Event
	var kind, mentions string
	var at int64
	var format sql.NullString
	var data []byte
	err := sc.Scan(&ev.ConvID, &ev.Seq, &kind, &ev.Actor, &at, &ev.MsgID, &format, &data, &mentions, &ev.Target, &ev.Name)
	if err != nil {
		return ev, err
	}
	ev.Kind, ev.At = model.EventKind(kind), fromMS(at)
	if format.Valid {
		ev.Body = &model.Body{Format: format.String, Data: data}
	}
	if mentions != "" {
		if err := json.Unmarshal([]byte(mentions), &ev.Mentions); err != nil {
			return ev, fmt.Errorf("corrupt mentions on %s#%d: %w", ev.ConvID, ev.Seq, err)
		}
	}
	return ev, nil
}

func queryEvents(q querier, query string, args ...any) ([]model.Event, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// appendTx assigns the next Seq and the current time and inserts ev. The
// caller's transaction must be immediate so the MAX read cannot go stale.
func (s *Store) appendTx(tx *sql.Tx, ev model.Event) (model.Event, error) {
	if err := requireConv(tx, ev.ConvID); err != nil {
		return ev, err
	}
	registersMsg := ev.Kind == model.MessagePosted && ev.MsgID != ""
	if registersMsg {
		dup, err := exists(tx, `SELECT 1 FROM msg_ids WHERE conv = ? AND msg_id = ?`, ev.ConvID, ev.MsgID)
		if err != nil {
			return ev, err
		}
		if dup {
			return ev, fmt.Errorf("%w: message %s", ErrExists, ev.MsgID)
		}
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE conv = ?`, ev.ConvID).Scan(&ev.Seq); err != nil {
		return ev, err
	}
	at := s.nowMS()
	ev.At = fromMS(at)

	var format, data any
	if ev.Body != nil {
		d := ev.Body.Data
		if d == nil {
			d = []byte{}
		}
		format, data = ev.Body.Format, d
	}
	mentions := ""
	if len(ev.Mentions) > 0 {
		b, err := json.Marshal(ev.Mentions)
		if err != nil {
			return ev, err
		}
		mentions = string(b)
	}
	_, err := tx.Exec(`INSERT INTO events (`+eventCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ConvID, ev.Seq, string(ev.Kind), ev.Actor, at, ev.MsgID, format, data, mentions, ev.Target, ev.Name)
	if err != nil {
		return ev, err
	}
	if registersMsg {
		_, err = tx.Exec(`INSERT INTO msg_ids (conv, msg_id, seq) VALUES (?, ?, ?)`, ev.ConvID, ev.MsgID, ev.Seq)
	}
	return ev, err
}

// Append stores ev as the next event of its conversation, replacing Seq and
// At. Callers that need message idempotency or author checks use PostMessage,
// EditMessage and DeleteMessage instead.
func (s *Store) Append(ev model.Event) (model.Event, error) {
	var out model.Event
	err := s.write(func(tx *sql.Tx) error {
		var err error
		out, err = s.appendTx(tx, ev)
		return err
	})
	return out, err
}

// cleanMentions normalises sender-chosen handles and drops invalid ones.
func cleanMentions(in []string) []string {
	var out []string
	for _, m := range in {
		if n := model.NormalizeName(m); model.ValidName(n) {
			out = append(out, n)
		}
	}
	return out
}

// PostMessage appends a message.posted event. Posting a msgID that already
// exists in the conversation returns the original event with duplicate set.
func (s *Store) PostMessage(conv, actor, msgID string, body model.Body, mentions []string) (ev model.Event, duplicate bool, err error) {
	a, err := cleanName(actor)
	if err != nil {
		return ev, false, err
	}
	if msgID == "" {
		return ev, false, errors.New("message id required")
	}
	err = s.write(func(tx *sql.Tx) error {
		var seq int64
		qerr := tx.QueryRow(`SELECT seq FROM msg_ids WHERE conv = ? AND msg_id = ?`, conv, msgID).Scan(&seq)
		switch {
		case qerr == nil:
			evs, err := queryEvents(tx, `SELECT `+eventCols+` FROM events WHERE conv = ? AND seq = ?`, conv, seq)
			if err != nil || len(evs) != 1 {
				return fmt.Errorf("message %s: original event missing: %v", msgID, err)
			}
			ev, duplicate = evs[0], true
			return nil
		case !errors.Is(qerr, sql.ErrNoRows):
			return qerr
		}
		var err error
		ev, err = s.appendTx(tx, model.Event{
			ConvID: conv, Kind: model.MessagePosted, Actor: a, MsgID: msgID,
			Body: &body, Mentions: cleanMentions(mentions),
		})
		return err
	})
	return ev, duplicate, err
}

// authorOf checks that msgID exists, is not deleted, and was posted by actor.
func authorOf(tx *sql.Tx, conv, actor, msgID string) error {
	var deleted int
	var author string
	err := tx.QueryRow(`SELECT m.deleted, e.actor FROM msg_ids m
		JOIN events e ON e.conv = m.conv AND e.seq = m.seq
		WHERE m.conv = ? AND m.msg_id = ?`, conv, msgID).Scan(&deleted, &author)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && deleted != 0) {
		return notFound("message " + msgID)
	}
	if err != nil {
		return err
	}
	if author != actor {
		return fmt.Errorf("%w: only the author may change message %s", ErrForbidden, msgID)
	}
	return nil
}

// blankMessage erases the body of the original post and every earlier edit.
func blankMessage(tx *sql.Tx, conv, msgID string) error {
	_, err := tx.Exec(`UPDATE events SET body_format = NULL, body_data = NULL
		WHERE conv = ? AND msg_id = ? AND kind IN (?, ?)`,
		conv, msgID, string(model.MessagePosted), string(model.MessageEdited))
	return err
}

// EditMessage replaces a message's body. Earlier bodies are erased, not kept.
func (s *Store) EditMessage(conv, actor, msgID string, body model.Body, mentions []string) (model.Event, error) {
	a, err := cleanName(actor)
	if err != nil {
		return model.Event{}, err
	}
	var ev model.Event
	err = s.write(func(tx *sql.Tx) error {
		if err := authorOf(tx, conv, a, msgID); err != nil {
			return err
		}
		if err := blankMessage(tx, conv, msgID); err != nil {
			return err
		}
		ev, err = s.appendTx(tx, model.Event{
			ConvID: conv, Kind: model.MessageEdited, Actor: a, MsgID: msgID,
			Body: &body, Mentions: cleanMentions(mentions),
		})
		return err
	})
	return ev, err
}

// DeleteMessage erases a message's bodies and appends message.deleted.
func (s *Store) DeleteMessage(conv, actor, msgID string) (model.Event, error) {
	a, err := cleanName(actor)
	if err != nil {
		return model.Event{}, err
	}
	var ev model.Event
	err = s.write(func(tx *sql.Tx) error {
		if err := authorOf(tx, conv, a, msgID); err != nil {
			return err
		}
		if err := blankMessage(tx, conv, msgID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE msg_ids SET deleted = 1 WHERE conv = ? AND msg_id = ?`, conv, msgID); err != nil {
			return err
		}
		ev, err = s.appendTx(tx, model.Event{ConvID: conv, Kind: model.MessageDeleted, Actor: a, MsgID: msgID})
		return err
	})
	return ev, err
}

// History returns up to limit events with Seq below beforeSeq (0 means the
// newest), in ascending order. A limit of zero or less means 50.
func (s *Store) History(conv string, beforeSeq int64, limit int) ([]model.Event, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	query := `SELECT ` + eventCols + ` FROM events WHERE conv = ?`
	args := []any{conv}
	if beforeSeq > 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	evs, err := queryEvents(s.db, query+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
	}
	return evs, nil
}

// After returns every event with Seq greater than seq, ascending.
func (s *Store) After(conv string, seq int64) ([]model.Event, error) {
	return queryEvents(s.db, `SELECT `+eventCols+` FROM events WHERE conv = ? AND seq > ? ORDER BY seq`, conv, seq)
}

// afterLimit is After capped at limit events.
func (s *Store) afterLimit(conv string, seq int64, limit int) ([]model.Event, error) {
	return queryEvents(s.db, `SELECT `+eventCols+` FROM events WHERE conv = ? AND seq > ? ORDER BY seq LIMIT ?`, conv, seq, limit)
}

// SetRead records how far handle has read a conversation. The marker only
// moves forward.
func (s *Store) SetRead(handle, conv string, seq int64) error {
	h, err := cleanName(handle)
	if err != nil {
		return err
	}
	return s.write(func(tx *sql.Tx) error {
		if err := requirePerson(tx, h); err != nil {
			return err
		}
		if err := requireConv(tx, conv); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO reads (handle, conv, seq) VALUES (?, ?, ?)
			ON CONFLICT (handle, conv) DO UPDATE SET seq = MAX(seq, excluded.seq)`, h, conv, seq)
		return err
	})
}

// PurgeOlderThan erases the bodies of events older than d and returns how many
// it blanked. Rows, Seq values and membership events stay.
func (s *Store) PurgeOlderThan(d time.Duration) (int, error) {
	var n int64
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE events SET body_format = NULL, body_data = NULL
			WHERE at < ? AND body_format IS NOT NULL`, toMS(s.now().Add(-d)))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}
