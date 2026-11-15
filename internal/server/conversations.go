package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

// Conversation returns one conversation with Members and LastSeq filled.
func (s *Store) Conversation(id string) (model.Conversation, error) {
	c, err := s.conversation(id)
	if err != nil {
		return c, err
	}
	c.LastSeq, err = s.lastSeq(id)
	return c, err
}

// convExists reports whether a conversation with the id exists.
func (s *Store) convExists(id string) (bool, error) {
	return exists(s.db, `SELECT 1 FROM conversations WHERE id = ?`, id)
}

// conversation is Conversation without LastSeq.
func (s *Store) conversation(id string) (model.Conversation, error) {
	c, err := scanConv(s.db.QueryRow(`SELECT `+convCols+` FROM conversations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound("conversation " + id)
	}
	if err != nil {
		return c, err
	}
	c.Members, err = s.Members(id)
	return c, err
}

// Members lists a conversation's members, ordered by handle.
func (s *Store) Members(conv string) ([]string, error) {
	var out []string
	err := eachRow(s.db, func(r *sql.Rows) error {
		var h string
		err := r.Scan(&h)
		out = append(out, h)
		return err
	}, `SELECT handle FROM members WHERE conv = ? ORDER BY handle`, conv)
	return out, err
}

// lastSeq is the conversation's highest Seq, zero when it has no events.
func (s *Store) lastSeq(conv string) (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events WHERE conv = ?`, conv).Scan(&seq)
	return seq, err
}

// listConvs runs a query selecting convCols and fills Members and LastSeq on
// every row with one query each, whatever the number of rows.
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
	if len(out) == 0 {
		return out, nil
	}

	ids := make([]string, len(out))
	for i, c := range out {
		ids[i] = c.ID
	}
	// One bound JSON array instead of a placeholder per id, so the number of
	// conversations is not limited by SQLite's variable count.
	idList, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	members := map[string][]string{}
	err = eachRow(s.db, func(r *sql.Rows) error {
		var conv, h string
		if err := r.Scan(&conv, &h); err != nil {
			return err
		}
		members[conv] = append(members[conv], h)
		return nil
	}, `SELECT conv, handle FROM members WHERE conv IN (SELECT value FROM json_each(?))
		ORDER BY conv, handle`, string(idList))
	if err != nil {
		return nil, err
	}
	last, err := seqByConv(s.db, `SELECT conv, MAX(seq) FROM events
		WHERE conv IN (SELECT value FROM json_each(?)) GROUP BY conv`, string(idList))
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Members, out[i].LastSeq = members[out[i].ID], last[out[i].ID]
	}
	return out, nil
}

// eachRow runs query and calls fn for every row.
func eachRow(q querier, fn func(*sql.Rows) error, query string, args ...any) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// seqByConv runs a query selecting (conv, seq) pairs into a map.
func seqByConv(q querier, query string, args ...any) (map[string]int64, error) {
	out := map[string]int64{}
	err := eachRow(q, func(r *sql.Rows) error {
		var conv string
		var seq int64
		if err := r.Scan(&conv, &seq); err != nil {
			return err
		}
		out[conv] = seq
		return nil
	}, query, args...)
	return out, err
}

// OpenRooms lists rooms anyone may join.
func (s *Store) OpenRooms() ([]model.Conversation, error) {
	return s.listConvs(`SELECT ` + convCols + ` FROM conversations WHERE kind = 'room' AND open = 1 ORDER BY name`)
}

// openRoomsWithout lists the open rooms handle does not belong to.
func (s *Store) openRoomsWithout(handle string) ([]model.Conversation, error) {
	return s.listConvs(`SELECT `+convCols+` FROM conversations WHERE kind = 'room' AND open = 1
		AND id NOT IN (SELECT conv FROM members WHERE handle = ?) ORDER BY name`, handle)
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
	if err != nil || len(out) == 0 {
		return out, err
	}
	reads, err := seqByConv(s.db, `SELECT conv, seq FROM reads WHERE handle = ?`, h)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ReadSeq = reads[out[i].ID]
	}
	return out, nil
}

// readSeq is handle's read marker in conv, zero when none is stored.
func (s *Store) readSeq(handle, conv string) (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE((SELECT seq FROM reads WHERE handle = ? AND conv = ?), 0)`, handle, conv).Scan(&seq)
	return seq, err
}

// convIDsFor is the set of conversation ids handle belongs to.
func (s *Store) convIDsFor(handle string) (map[string]bool, error) {
	out := map[string]bool{}
	err := eachRow(s.db, func(r *sql.Rows) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		out[id] = true
		return nil
	}, `SELECT conv FROM members WHERE handle = ?`, handle)
	return out, err
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
		if err := requireAffected(res, h+" is not a member of "+conv); err != nil {
			return err
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
