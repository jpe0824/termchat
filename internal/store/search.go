package store

import (
	"strings"
	"unicode"
)

// ftsQuery turns user input into an FTS5 expression that matches messages
// holding every word. Each word becomes a quoted phrase, so operators and
// punctuation in the input are literal. Words without a letter or digit
// tokenise to nothing and are dropped. It returns "" when no word is left.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.Fields(q) {
		if !strings.ContainsFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " ")
}

// Search returns the messages whose current text contains every word of q,
// newest first, at most limit (<= 0 means 50). Conversations the user has left
// are included.
func (s *Store) Search(q string, limit int) ([]Message, error) {
	expr := ftsQuery(q)
	if expr == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	rows, err := s.db.Query(`SELECT `+messageCols+`
		FROM msg_fts f JOIN messages m ON m.id = f.rowid LEFT JOIN people p ON p.handle = m.author
		WHERE msg_fts MATCH ? ORDER BY m.at DESC, m.seq DESC, m.conv LIMIT ?`, expr, limit)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}
