package store

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order; the database's user_version records how
// many have run. Entries are append-only: a shipped migration never changes.
var migrations = []string{
	`
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE people (
	handle       TEXT PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	agent        INTEGER NOT NULL DEFAULT 0,
	admin        INTEGER NOT NULL DEFAULT 0,
	owner        TEXT NOT NULL DEFAULT ''
);

-- snap_seq is the LastSeq of the snapshot that supplied name and the rows of
-- conv_members; held membership and rename events newer than it override them.
CREATE TABLE conversations (
	id       TEXT PRIMARY KEY,
	kind     TEXT NOT NULL,
	name     TEXT NOT NULL DEFAULT '',
	open     INTEGER NOT NULL DEFAULT 0,
	creator  TEXT NOT NULL DEFAULT '',
	last_seq INTEGER NOT NULL DEFAULT 0,
	read_seq INTEGER NOT NULL DEFAULT 0,
	snap_seq INTEGER NOT NULL DEFAULT 0,
	gone     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE conv_members (
	conv   TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	handle TEXT NOT NULL,
	PRIMARY KEY (conv, handle)
);

-- body_format is NULL exactly when the event holds no body.
CREATE TABLE events (
	conv        TEXT NOT NULL,
	seq         INTEGER NOT NULL,
	kind        TEXT NOT NULL,
	actor       TEXT NOT NULL,
	at          INTEGER NOT NULL,
	msg_id      TEXT NOT NULL DEFAULT '',
	body_format TEXT,
	body_data   BLOB,
	mentions    TEXT NOT NULL DEFAULT '',
	target      TEXT NOT NULL DEFAULT '',
	name        TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (conv, seq)
);
CREATE INDEX events_msg ON events(conv, msg_id) WHERE msg_id <> '';

-- messages is derived from events: one row per message whose posted event is
-- held, rebuilt whenever an event for that message changes.
CREATE TABLE messages (
	id       INTEGER PRIMARY KEY,
	conv     TEXT NOT NULL,
	msg_id   TEXT NOT NULL,
	seq      INTEGER NOT NULL,
	at       INTEGER NOT NULL,
	author   TEXT NOT NULL,
	text     TEXT NOT NULL DEFAULT '',
	mentions TEXT NOT NULL DEFAULT '',
	edited   INTEGER NOT NULL DEFAULT 0,
	deleted  INTEGER NOT NULL DEFAULT 0,
	UNIQUE (conv, msg_id)
);
CREATE INDEX messages_seq ON messages(conv, seq);

-- Rows are keyed by messages.id and exist only for visible text/plain messages.
CREATE VIRTUAL TABLE msg_fts USING fts5(text);

CREATE TABLE outbox (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	msg_id     TEXT NOT NULL UNIQUE,
	conv       TEXT NOT NULL,
	text       TEXT NOT NULL,
	mentions   TEXT NOT NULL DEFAULT '',
	source     TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	state      TEXT NOT NULL,
	err        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX outbox_conv ON outbox(conv, created_at, id);
`,
	`
-- skipped holds sequence numbers whose event was received but could not be
-- stored. They count as held when finding the contiguous run, so a sync is
-- not asked for the same event again; a valid copy replaces the row.
CREATE TABLE skipped (
	conv TEXT NOT NULL,
	seq  INTEGER NOT NULL,
	PRIMARY KEY (conv, seq)
);
`,
	`
-- events_kind_at answers a conversation's newest message event of each kind
-- with one seek; messages_unread covers the unread count, so it reads only
-- the index entries after the read position.
CREATE INDEX events_kind_at ON events(conv, kind, at);
CREATE INDEX messages_unread ON messages(conv, seq, author, deleted);
`,
}

// migrate brings db up to the latest schema inside one immediate transaction,
// re-reading the version there so concurrent opens cannot both migrate.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA takes no bound parameters.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
