package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/jpe0824/termchat/internal/model"
)

const convCols = `id, kind, COALESCE(name, ''), open, creator`

func scanConv(sc scanner) (model.Conversation, error) {
	var c model.Conversation
	var kind string
	var open int
	err := sc.Scan(&c.ID, &kind, &c.Name, &open, &c.Creator)
	c.Kind, c.Open = model.ConvKind(kind), open != 0
	return c, err
}

// fillConv sets Members and LastSeq.
func fillConv(q querier, c *model.Conversation) error {
	rows, err := q.Query(`SELECT handle FROM members WHERE conv = ? ORDER BY handle`, c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Members = nil
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return err
		}
		c.Members = append(c.Members, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return q.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events WHERE conv = ?`, c.ID).Scan(&c.LastSeq)
}

// Conversation returns one conversation with Members and LastSeq filled.
func (s *Store) Conversation(id string) (model.Conversation, error) {
	c, err := scanConv(s.db.QueryRow(`SELECT `+convCols+` FROM conversations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound("conversation " + id)
	}
	if err != nil {
		return c, err
	}
	return c, fillConv(s.db, &c)
}

func (s *Store) listConvs(query string, args ...any) ([]model.Conversation, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var out []model.Conversation
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for i := range out {
		if err := fillConv(s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// OpenRooms lists rooms anyone may join.
func (s *Store) OpenRooms() ([]model.Conversation, error) {
	return s.listConvs(`SELECT ` + convCols + ` FROM conversations WHERE kind = 'room' AND open = 1 ORDER BY name`)
}

// Rooms lists every room, open or private.
func (s *Store) Rooms() ([]model.Conversation, error) {
	return s.listConvs(`SELECT ` + convCols + ` FROM conversations WHERE kind = 'room' ORDER BY name`)
}

// ConversationsFor lists the conversations handle belongs to, with that
// person's ReadSeq.
func (s *Store) ConversationsFor(handle string) ([]model.Conversation, error) {
	h, err := cleanName(handle)
	if err != nil {
		return nil, err
	}
	out, err := s.listConvs(`SELECT `+convCols+` FROM conversations
		WHERE id IN (SELECT conv FROM members WHERE handle = ?) ORDER BY id`, h)
	if err != nil {
		return nil, err
	}
	for i := range out {
		err := s.db.QueryRow(`SELECT COALESCE(
			(SELECT seq FROM reads WHERE handle = ? AND conv = ?), 0)`, h, out[i].ID).Scan(&out[i].ReadSeq)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readSeq is handle's read marker in conv, zero when none is stored.
func (s *Store) readSeq(handle, conv string) (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE((SELECT seq FROM reads WHERE handle = ? AND conv = ?), 0)`, handle, conv).Scan(&seq)
	return seq, err
}

// IsMember reports whether handle belongs to the conversation.
func (s *Store) IsMember(conv, handle string) (bool, error) {
	h, err := cleanName(handle)
	if err != nil {
		return false, err
	}
	return exists(s.db, `SELECT 1 FROM members WHERE conv = ? AND handle = ?`, conv, h)
}

// OpenDM returns the direct conversation between a and b, creating it on
// first use. Its id is derived from the sorted handles, so the order of
// arguments does not matter.
func (s *Store) OpenDM(a, b string) (model.Conversation, error) {
	ha, err := cleanName(a)
	if err != nil {
		return model.Conversation{}, err
	}
	hb, err := cleanName(b)
	if err != nil {
		return model.Conversation{}, err
	}
	if ha == hb {
		return model.Conversation{}, fmt.Errorf("%w: cannot message yourself", ErrInvalidName)
	}
	pair := []string{ha, hb}
	sort.Strings(pair)
	id := "dm:" + pair[0] + ":" + pair[1]
	err = s.write(func(tx *sql.Tx) error {
		for _, h := range pair {
			if err := requirePerson(tx, h); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conversations (id, kind, open, creator, created_at)
			VALUES (?, 'dm', 0, '', ?)`, id, s.nowMS()); err != nil {
			return err
		}
		for _, h := range pair {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO members (conv, handle) VALUES (?, ?)`, id, h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Conversation{}, err
	}
	return s.Conversation(id)
}

// CreateRoom makes a room and joins its creator, appending the join event.
func (s *Store) CreateRoom(name string, open bool, creator string) (model.Conversation, error) {
	n, err := cleanName(name)
	if err != nil {
		return model.Conversation{}, err
	}
	c, err := cleanName(creator)
	if err != nil {
		return model.Conversation{}, err
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return model.Conversation{}, err
	}
	id := "r_" + hex.EncodeToString(raw[:])
	err = s.write(func(tx *sql.Tx) error {
		if err := requirePerson(tx, c); err != nil {
			return err
		}
		taken, err := exists(tx, `SELECT 1 FROM conversations WHERE name = ?`, n)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: room %s", ErrExists, n)
		}
		if _, err := tx.Exec(`INSERT INTO conversations (id, kind, name, open, creator, created_at)
			VALUES (?, 'room', ?, ?, ?, ?)`, id, n, boolInt(open), c, s.nowMS()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO members (conv, handle) VALUES (?, ?)`, id, c); err != nil {
			return err
		}
		_, err = s.appendTx(tx, model.Event{ConvID: id, Kind: model.MemberJoined, Actor: c, Target: c})
		return err
	})
	if err != nil {
		return model.Conversation{}, err
	}
	return s.Conversation(id)
}

// AddMember joins handle to a conversation and appends member.joined.
func (s *Store) AddMember(conv, actor, handle string) (model.Event, error) {
	a, err := cleanName(actor)
	if err != nil {
		return model.Event{}, err
	}
	h, err := cleanName(handle)
	if err != nil {
		return model.Event{}, err
	}
	var ev model.Event
	err = s.write(func(tx *sql.Tx) error {
		if err := requireConv(tx, conv); err != nil {
			return err
		}
		if err := requirePerson(tx, h); err != nil {
			return err
		}
		already, err := exists(tx, `SELECT 1 FROM members WHERE conv = ? AND handle = ?`, conv, h)
		if err != nil {
			return err
		}
		if already {
			return fmt.Errorf("%w: %s is already a member", ErrExists, h)
		}
		if _, err := tx.Exec(`INSERT INTO members (conv, handle) VALUES (?, ?)`, conv, h); err != nil {
			return err
		}
		ev, err = s.appendTx(tx, model.Event{ConvID: conv, Kind: model.MemberJoined, Actor: a, Target: h})
		return err
	})
	return ev, err
}

// RemoveMember removes handle from a conversation and appends member.left.
func (s *Store) RemoveMember(conv, actor, handle string) (model.Event, error) {
	a, err := cleanName(actor)
	if err != nil {
		return model.Event{}, err
	}
	h, err := cleanName(handle)
	if err != nil {
		return model.Event{}, err
	}
	var ev model.Event
	err = s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM members WHERE conv = ? AND handle = ?`, conv, h)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound(h + " is not a member of " + conv)
		}
		ev, err = s.appendTx(tx, model.Event{ConvID: conv, Kind: model.MemberLeft, Actor: a, Target: h})
		return err
	})
	return ev, err
}

// RenameRoom changes a room's name and appends room.renamed.
func (s *Store) RenameRoom(conv, actor, name string) (model.Event, error) {
	a, err := cleanName(actor)
	if err != nil {
		return model.Event{}, err
	}
	n, err := cleanName(name)
	if err != nil {
		return model.Event{}, err
	}
	var ev model.Event
	err = s.write(func(tx *sql.Tx) error {
		isRoom, err := exists(tx, `SELECT 1 FROM conversations WHERE id = ? AND kind = 'room'`, conv)
		if err != nil {
			return err
		}
		if !isRoom {
			return notFound("room " + conv)
		}
		taken, err := exists(tx, `SELECT 1 FROM conversations WHERE name = ?`, n)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: room %s", ErrExists, n)
		}
		if _, err := tx.Exec(`UPDATE conversations SET name = ? WHERE id = ?`, n, conv); err != nil {
			return err
		}
		ev, err = s.appendTx(tx, model.Event{ConvID: conv, Kind: model.RoomRenamed, Actor: a, Name: n})
		return err
	})
	return ev, err
}
