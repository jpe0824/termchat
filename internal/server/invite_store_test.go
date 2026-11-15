package server

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

// An invite written before invites recorded their kind cannot say whether it
// was for a new person or a new device, so it is refused, and the database it
// lives in still opens.
func TestInviteFromBeforeKindsIsRefused(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:2] {
		if _, err := db.Exec(m); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	expires := time.Now().Add(time.Hour).UnixMilli()
	for _, q := range []string{
		`PRAGMA user_version = 2`,
		`INSERT INTO people (handle, created_at) VALUES ('ann', 1)`,
		fmt.Sprintf(`INSERT INTO invites (code, handle, expires_at) VALUES ('OLDNEWCODE', 'dana', %d)`, expires),
		fmt.Sprintf(`INSERT INTO invites (code, handle, expires_at) VALUES ('OLDDEVCODE', 'ann', %d)`, expires),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()

	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore on a version 2 database: %v", err)
	}
	defer s.Close()
	for _, code := range []string{"OLDNEWCODE", "OLDDEVCODE"} {
		if _, err := s.RedeemInvite(code, "fp-"+code, []byte("k")); !errors.Is(err, ErrInviteInvalid) {
			t.Fatalf("redeem %s: %v, want ErrInviteInvalid", code, err)
		}
	}
	if _, err := s.Person("dana"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy invite created a person: %v", err)
	}
	if keys, _ := s.Keys("ann"); len(keys) != 0 {
		t.Fatalf("legacy invite added a key: %v", keys)
	}
}

func TestInviteKindsAreCheckedWhenRedeemed(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")

	// A device invite needs the account to exist when it is made.
	if _, err := s.CreateDeviceInvite("nobody", time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device invite for missing handle: %v, want ErrNotFound", err)
	}
	// A new-person invite needs the handle to be free when it is made.
	if _, err := s.CreateInvite("ann", "", time.Hour); !errors.Is(err, ErrExists) {
		t.Fatalf("person invite for existing handle: %v, want ErrExists", err)
	}
	if _, err := s.CreateAgentInvite("ann", "", "ann", time.Hour); !errors.Is(err, ErrExists) {
		t.Fatalf("agent invite for existing handle: %v, want ErrExists", err)
	}

	// Made while the handle was free, redeemed after it was taken.
	early, err := s.CreateInvite("dana", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	people(t, s, "dana")
	if _, err := s.RedeemInvite(early, "fp1", []byte("k")); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("person invite for a taken handle: %v, want ErrInviteInvalid", err)
	}
	if keys, _ := s.Keys("dana"); len(keys) != 0 {
		t.Fatalf("keys added to dana: %v", keys)
	}
}
