package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const defaultLimit = 50

// messageCols selects a Message from messages m, with the author's identity
// from people p.
const messageCols = `m.conv, m.msg_id, m.author, m.seq, m.at, m.text, m.mentions, m.edited, m.deleted,
	COALESCE(p.agent, 0), COALESCE(NULLIF(p.display_name, ''), m.author)`

const messageFrom = ` FROM messages m LEFT JOIN people p ON p.handle = m.author `

func scanMessage(sc interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var at int64
	var mentions string
	var edited, deleted, agent int
	if err := sc.Scan(&m.ConvID, &m.MsgID, &m.Author, &m.Seq, &at, &m.Text, &mentions,
		&edited, &deleted, &agent, &m.AuthorName); err != nil {
		return Message{}, err
	}
	m.At = fromMS(at)
	m.Mentions = splitHandles(mentions)
	m.Edited, m.Deleted, m.Agent = edited != 0, deleted != 0, agent != 0
	return m, nil
}

func collectMessages(rows *sql.Rows) ([]Message, error) {
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Messages returns messages of conv with Seq < beforeSeq (0 means from the
// newest), at most limit of them (<= 0 means 50), oldest first. When beforeSeq
// is 0 the conversation's outbox items follow, in creation order.
func (s *Store) Messages(conv string, beforeSeq int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	rows, err := s.db.Query(`SELECT `+messageCols+messageFrom+`
		WHERE m.conv = ? AND (? = 0 OR m.seq < ?) ORDER BY m.seq DESC LIMIT ?`,
		conv, beforeSeq, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	out, err := collectMessages(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if beforeSeq != 0 {
		return out, nil
	}
	items, err := s.Outbox(conv)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		out = append(out, s.outboxMessage(it))
	}
	return out, nil
}

// ByMsgID returns one message of conv, from the stored messages or, failing
// that, from the outbox. It returns ErrNotFound when neither holds it.
func (s *Store) ByMsgID(conv, msgID string) (Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+messageCols+messageFrom+`
		WHERE m.conv = ? AND m.msg_id = ?`, conv, msgID))
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, err
	}
	items, err := s.queryOutbox(`WHERE conv = ? AND msg_id = ?`, conv, msgID)
	if err != nil {
		return Message{}, err
	}
	if len(items) == 0 {
		return Message{}, fmt.Errorf("%w: message %s in %s", ErrNotFound, msgID, conv)
	}
	return s.outboxMessage(items[0]), nil
}
