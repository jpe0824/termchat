package server

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order; the database's user_version records how
// many have run. Entries are append-only: a shipped migration never changes.
var migrations = []string{
	`
CREATE TABLE people (
	handle       TEXT PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	agent        INTEGER NOT NULL DEFAULT 0,
	admin        INTEGER NOT NULL DEFAULT 0,
	owner        TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL
);

CREATE TABLE keys (
	fingerprint TEXT PRIMARY KEY,
	handle      TEXT NOT NULL REFERENCES people(handle) ON DELETE CASCADE,
	pub         BLOB NOT NULL,
	added_at    INTEGER NOT NULL
);
CREATE INDEX keys_handle ON keys(handle);

CREATE TABLE invites (
	code         TEXT PRIMARY KEY,
	handle       TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	agent        INTEGER NOT NULL DEFAULT 0,
	owner        TEXT NOT NULL DEFAULT '',
	expires_at   INTEGER NOT NULL,
	used_at      INTEGER
);

CREATE TABLE conversations (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	name       TEXT UNIQUE,
	open       INTEGER NOT NULL DEFAULT 0,
	creator    TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE TABLE members (
	conv   TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	handle TEXT NOT NULL REFERENCES people(handle) ON DELETE CASCADE,
	PRIMARY KEY (conv, handle)
);
CREATE INDEX members_handle ON members(handle);

CREATE TABLE events (
	conv        TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
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
CREATE INDEX events_at ON events(at);

CREATE TABLE msg_ids (
	conv    TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	msg_id  TEXT NOT NULL,
	seq     INTEGER NOT NULL,
	deleted INTEGER NOT NULL DEFAULT 0,
	UNIQUE (conv, msg_id)
);

CREATE TABLE reads (
	handle TEXT NOT NULL REFERENCES people(handle) ON DELETE CASCADE,
	conv   TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	seq    INTEGER NOT NULL,
	PRIMARY KEY (handle, conv)
);
`,
	// A removed person keeps their row so the handle can never be registered
	// again; every lookup treats removed_at IS NOT NULL as absent.
	`ALTER TABLE people ADD COLUMN removed_at INTEGER`,
}

// migrate brings db up to the latest schema. It runs in an immediate
// transaction and re-reads the version inside it, so two processes opening a
// fresh file at once cannot both apply a migration.
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
