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

// personColsP is personCols qualified with the table alias p.
var personColsP = "p." + strings.ReplaceAll(personCols, ", ", ", p.")

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
// ErrLastAdmin and granting to an agent account with ErrAgent.
func (s *Store) SetAdmin(handle string, admin bool) error {
	h, err := cleanName(handle)
	if err != nil {
		return err
	}
	return s.write(func(tx *sql.Tx) error {
		if admin {
			agent, err := exists(tx, `SELECT 1 FROM people WHERE handle = ? AND agent = 1 AND removed_at IS NULL`, h)
			if err != nil {
				return err
			}
			if agent {
				return fmt.Errorf("%w: %s", ErrAgent, h)
			}
		} else if err := guardLastAdmin(tx, h); err != nil {
			return err
		}
		res, err := tx.Exec(`UPDATE people SET admin = ? WHERE handle = ? AND removed_at IS NULL`, boolInt(admin), h)
		if err != nil {
			return err
		}
		return requireAffected(res, "person "+h)
	})
}

// lastAdminQuery is true when its handle is the only remaining admin.
const lastAdminQuery = `SELECT
	COALESCE((SELECT admin FROM people WHERE handle = ? AND removed_at IS NULL), 0) = 1
	AND (SELECT COUNT(*) FROM people WHERE admin = 1 AND removed_at IS NULL) <= 1`

func isLastAdmin(q querier, handle string) (bool, error) {
	var last int
	err := q.QueryRow(lastAdminQuery, handle).Scan(&last)
	return last != 0, err
}

// IsLastAdmin reports whether handle is the only remaining admin. The answer
// can be stale by the time the caller acts; changes that must not remove the
// last admin rely on the check inside their own transaction.
func (s *Store) IsLastAdmin(handle string) (bool, error) {
	h, err := cleanName(handle)
	if err != nil {
		return false, err
	}
	return isLastAdmin(s.db, h)
}

// guardLastAdmin returns ErrLastAdmin if handle is the only remaining admin.
// It runs inside the same immediate transaction as the change it guards, so
// two writers, even in different processes, cannot each see a second admin.
func guardLastAdmin(tx *sql.Tx, handle string) error {
	last, err := isLastAdmin(tx, handle)
	if err != nil {
		return err
	}
	if last {
		return ErrLastAdmin
	}
	return nil
}

// RemovePerson deletes a person's keys, memberships and read markers and
// tombstones the handle: it can never be registered again, so nobody can
// inherit the removed person's DM history. Events they authored stay in the
// log. Removing the only remaining admin fails with ErrLastAdmin.
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
		if err := requireAffected(res, "person "+h); err != nil {
			return err
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
	p, err := scanPerson(s.db.QueryRow(`SELECT `+personColsP+`
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
	return requireAffected(res, "key "+fingerprint)
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

// Invite kinds. A person invite creates its handle and fails once the handle
// exists; a device invite adds a key to its handle and fails unless the handle
// exists. The kind is checked when the code is redeemed, because a handle can
// be taken or removed while an invite waits. An invite with any other kind,
// including those stored before kinds were recorded, is never redeemable.
const (
	invitePerson = "person"
	inviteDevice = "device"
)

// CreateInvite issues a single-use code that creates the person handle when
// redeemed before ttl passes. A handle that exists or was removed is refused
// with ErrExists.
func (s *Store) CreateInvite(handle, displayName string, ttl time.Duration) (string, error) {
	return s.createInvite(handle, displayName, invitePerson, false, "", ttl)
}

// CreateAgentInvite is CreateInvite for an agent identity owned by owner.
func (s *Store) CreateAgentInvite(handle, displayName, owner string, ttl time.Duration) (string, error) {
	o, err := cleanName(owner)
	if err != nil {
		return "", err
	}
	return s.createInvite(handle, displayName, invitePerson, true, o, ttl)
}

// CreateDeviceInvite issues a single-use code that adds the redeeming key to
// the existing person handle. A handle with no account is refused with
// ErrNotFound.
func (s *Store) CreateDeviceInvite(handle string, ttl time.Duration) (string, error) {
	return s.createInvite(handle, "", inviteDevice, false, "", ttl)
}

func (s *Store) createInvite(handle, displayName, kind string, agent bool, owner string, ttl time.Duration) (string, error) {
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
	switch _, err := personByHandle(s.db, h); {
	case err == nil && kind == invitePerson:
		return "", fmt.Errorf("%w: person %s", ErrExists, h)
	case errors.Is(err, ErrNotFound) && kind == inviteDevice:
		return "", err
	case err != nil && !errors.Is(err, ErrNotFound):
		return "", err
	}
	code, err := newInviteCode()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO invites (code, handle, display_name, agent, owner, kind, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, code, h, displayName, boolInt(agent), owner, kind, toMS(s.now().Add(ttl)))
	return code, err
}

// RedeemInvite consumes a code and registers the key, either as a new person
// or as another device of an existing one, as the invite's kind says. An
// invite whose kind no longer matches its handle fails with ErrInviteInvalid.
// A fingerprint that is already registered fails with ErrExists. Both leave
// the invite unused.
func (s *Store) RedeemInvite(code, fingerprint string, pub []byte) (model.Person, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	var person model.Person
	err := s.write(func(tx *sql.Tx) error {
		var inv model.Person
		var agent int
		var kind string
		var expires int64
		var used sql.NullInt64
		err := tx.QueryRow(`SELECT handle, display_name, agent, owner, kind, expires_at, used_at
			FROM invites WHERE code = ?`, code).Scan(&inv.Handle, &inv.DisplayName, &agent, &inv.Owner, &kind, &expires, &used)
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

		switch kind {
		case inviteDevice:
			person, err = personByHandle(tx, inv.Handle)
			if errors.Is(err, ErrNotFound) {
				return ErrInviteInvalid
			}
		case invitePerson:
			person = inv
			err = insertPerson(tx, person, now)
			if errors.Is(err, ErrExists) {
				return ErrInviteInvalid
			}
		default:
			return ErrInviteInvalid
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
