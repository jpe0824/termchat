package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

// Key is a registered device key.
type Key struct {
	Fingerprint string
	AddedAt     time.Time
}

const personCols = `handle, display_name, agent, admin, owner`

func scanPerson(sc scanner) (model.Person, error) {
	var p model.Person
	var agent, admin int
	err := sc.Scan(&p.Handle, &p.DisplayName, &agent, &admin, &p.Owner)
	p.Agent, p.Admin = agent != 0, admin != 0
	return p, err
}

func personByHandle(q querier, handle string) (model.Person, error) {
	p, err := scanPerson(q.QueryRow(`SELECT `+personCols+` FROM people WHERE handle = ? AND removed_at IS NULL`, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return p, notFound("person " + handle)
	}
	return p, err
}

func insertPerson(q querier, p model.Person, at int64) error {
	// Matches removed people too: their handles stay reserved.
	ok, err := exists(q, `SELECT 1 FROM people WHERE handle = ?`, p.Handle)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%w: person %s", ErrExists, p.Handle)
	}
	_, err = q.Exec(`INSERT INTO people (handle, display_name, agent, admin, owner, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, p.Handle, p.DisplayName, boolInt(p.Agent), boolInt(p.Admin), p.Owner, at)
	return err
}

func insertKey(q querier, handle, fingerprint string, pub []byte, at int64) error {
	ok, err := exists(q, `SELECT 1 FROM keys WHERE fingerprint = ?`, fingerprint)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%w: key %s", ErrExists, fingerprint)
	}
	if pub == nil {
		pub = []byte{}
	}
	_, err = q.Exec(`INSERT INTO keys (fingerprint, handle, pub, added_at) VALUES (?, ?, ?, ?)`,
		fingerprint, handle, pub, at)
	return err
}

// CreatePerson stores p with a normalised handle and owner.
func (s *Store) CreatePerson(p model.Person) error {
	var err error
	if p.Handle, err = cleanName(p.Handle); err != nil {
		return err
	}
	if p.Owner != "" {
		if p.Owner, err = cleanName(p.Owner); err != nil {
			return err
		}
	}
	return s.write(func(tx *sql.Tx) error { return insertPerson(tx, p, s.nowMS()) })
}

// Person returns the person with the given handle.
func (s *Store) Person(handle string) (model.Person, error) {
	h, err := cleanName(handle)
	if err != nil {
		return model.Person{}, err
	}
	return personByHandle(s.db, h)
}

// People returns everyone, ordered by handle.
func (s *Store) People() ([]model.Person, error) {
	rows, err := s.db.Query(`SELECT ` + personCols + ` FROM people WHERE removed_at IS NULL ORDER BY handle`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonCount returns the number of people, agents included.
func (s *Store) PersonCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM people WHERE removed_at IS NULL`).Scan(&n)
	return n, err
}

// SetAdmin grants or revokes admin rights. Revoking the last admin fails with
// ErrLastAdmin.
func (s *Store) SetAdmin(handle string, admin bool) error {
	h, err := cleanName(handle)
	if err != nil {
		return err
	}
	return s.write(func(tx *sql.Tx) error {
		if !admin {
			if err := guardLastAdmin(tx, h); err != nil {
				return err
			}
		}
		res, err := tx.Exec(`UPDATE people SET admin = ? WHERE handle = ? AND removed_at IS NULL`, boolInt(admin), h)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("person " + h)
		}
		return nil
	})
}

// guardLastAdmin returns ErrLastAdmin if handle is the only remaining admin.
// It runs inside the same immediate transaction as the change it guards, so
// two writers, even in different processes, cannot each see a second admin.
func guardLastAdmin(tx *sql.Tx, handle string) error {
	var isAdmin, admins int
	err := tx.QueryRow(`SELECT
		COALESCE((SELECT admin FROM people WHERE handle = ? AND removed_at IS NULL), 0),
		(SELECT COUNT(*) FROM people WHERE admin = 1 AND removed_at IS NULL)`, handle).Scan(&isAdmin, &admins)
	if err != nil {
		return err
	}
	if isAdmin == 1 && admins <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// RemovePerson, which fails with ErrLastAdmin for the only admin, deletes a person's keys, memberships and read markers and
// tombstones the handle: it can never be registered again, so nobody can
// inherit the removed person's DM history. Events they authored stay in the log.
func (s *Store) RemovePerson(handle string) error {
	h, err := cleanName(handle)
	if err != nil {
		return err
	}
	return s.write(func(tx *sql.Tx) error {
		if err := guardLastAdmin(tx, h); err != nil {
			return err
		}
		res, err := tx.Exec(`UPDATE people SET removed_at = ?, admin = 0
			WHERE handle = ? AND removed_at IS NULL`, s.nowMS(), h)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("person " + h)
		}
		for _, table := range []string{"keys", "members", "reads"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE handle = ?`, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddKey registers a device key for an existing person.
func (s *Store) AddKey(handle, fingerprint string, pub []byte) error {
	h, err := cleanName(handle)
	if err != nil {
		return err
	}
	return s.write(func(tx *sql.Tx) error {
		if err := requirePerson(tx, h); err != nil {
			return err
		}
		return insertKey(tx, h, fingerprint, pub, s.nowMS())
	})
}

// PersonByKey resolves a key fingerprint to its owner.
func (s *Store) PersonByKey(fingerprint string) (model.Person, error) {
	p, err := scanPerson(s.db.QueryRow(`SELECT p.handle, p.display_name, p.agent, p.admin, p.owner
		FROM keys k JOIN people p ON p.handle = k.handle
		WHERE k.fingerprint = ? AND p.removed_at IS NULL`, fingerprint))
	if errors.Is(err, sql.ErrNoRows) {
		return p, notFound("key " + fingerprint)
	}
	return p, err
}

// HasKey reports whether the fingerprint is registered to anyone.
func (s *Store) HasKey(fingerprint string) (bool, error) {
	return exists(s.db, `SELECT 1 FROM keys WHERE fingerprint = ?`, fingerprint)
}

// Keys lists a person's device keys, oldest first.
func (s *Store) Keys(handle string) ([]Key, error) {
	h, err := cleanName(handle)
	if err != nil {
		return nil, err
	}
	if err := requirePerson(s.db, h); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT fingerprint, added_at FROM keys WHERE handle = ?
		ORDER BY added_at, fingerprint`, h)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var at int64
		if err := rows.Scan(&k.Fingerprint, &at); err != nil {
			return nil, err
		}
		k.AddedAt = fromMS(at)
		out = append(out, k)
	}
	return out, rows.Err()
}

// RemoveKey deletes one device key.
func (s *Store) RemoveKey(fingerprint string) error {
	res, err := s.db.Exec(`DELETE FROM keys WHERE fingerprint = ?`, fingerprint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("key " + fingerprint)
	}
	return nil
}

// ---- invites ----

var inviteEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newInviteCode returns 10 base32 characters (50 bits) from crypto/rand.
func newInviteCode() (string, error) {
	var b [7]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return inviteEncoding.EncodeToString(b[:])[:10], nil
}

// CreateInvite issues a single-use code that expires after ttl. Redeeming it
// for an existing handle adds a device to that person.
func (s *Store) CreateInvite(handle, displayName string, ttl time.Duration) (string, error) {
	return s.createInvite(handle, displayName, false, "", ttl)
}

// CreateAgentInvite is CreateInvite for an agent identity owned by owner.
func (s *Store) CreateAgentInvite(handle, displayName, owner string, ttl time.Duration) (string, error) {
	o, err := cleanName(owner)
	if err != nil {
		return "", err
	}
	return s.createInvite(handle, displayName, true, o, ttl)
}

func (s *Store) createInvite(handle, displayName string, agent bool, owner string, ttl time.Duration) (string, error) {
	h, err := cleanName(handle)
	if err != nil {
		return "", err
	}
	reserved, err := exists(s.db, `SELECT 1 FROM people WHERE handle = ? AND removed_at IS NOT NULL`, h)
	if err != nil {
		return "", err
	}
	if reserved {
		return "", fmt.Errorf("%w: handle %s was removed", ErrExists, h)
	}
	code, err := newInviteCode()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO invites (code, handle, display_name, agent, owner, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, code, h, displayName, boolInt(agent), owner, toMS(s.now().Add(ttl)))
	return code, err
}

// RedeemInvite consumes a code and registers the key. A new handle becomes a
// person; an existing handle just gains the key. A fingerprint that is already
// registered fails with ErrExists and leaves the invite unused.
func (s *Store) RedeemInvite(code, fingerprint string, pub []byte) (model.Person, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	var person model.Person
	err := s.write(func(tx *sql.Tx) error {
		var inv model.Person
		var agent int
		var expires int64
		var used sql.NullInt64
		err := tx.QueryRow(`SELECT handle, display_name, agent, owner, expires_at, used_at
			FROM invites WHERE code = ?`, code).Scan(&inv.Handle, &inv.DisplayName, &agent, &inv.Owner, &expires, &used)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInviteInvalid
		}
		if err != nil {
			return err
		}
		now := s.nowMS()
		if used.Valid || expires <= now {
			return ErrInviteInvalid
		}
		inv.Agent = agent != 0

		person, err = personByHandle(tx, inv.Handle)
		if errors.Is(err, ErrNotFound) {
			person = inv
			err = insertPerson(tx, person, now)
		}
		if err != nil {
			return err
		}
		if err := insertKey(tx, person.Handle, fingerprint, pub, now); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE invites SET used_at = ? WHERE code = ?`, now, code)
		return err
	})
	if err != nil {
		return model.Person{}, err
	}
	return person, nil
}
