package server

import (
	"encoding/json"
	"time"
)

// SetClock replaces the clock used for the failed-authentication window.
func SetClock(s *Server, now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// SetRecheckInterval shortens the known-session key re-check ticker for
// sessions that start afterwards.
func SetRecheckInterval(s *Server, d time.Duration) {
	s.mu.Lock()
	s.recheck = d
	s.mu.Unlock()
}

// SetHandshakeTimeout shortens the SSH handshake deadline for connections
// accepted afterwards.
func SetHandshakeTimeout(s *Server, d time.Duration) {
	s.mu.Lock()
	s.handshake = d
	s.mu.Unlock()
}

// SetStoreClock replaces the clock the store uses for timestamps and invite
// expiry.
func SetStoreClock(st *Store, now func() time.Time) { st.now = now }

// Push queues an unsolicited frame on the session's ordered outbox.
func Push(sess *Session, typ string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sess.enqueue(typ, b)
	return nil
}

// LockCount is the number of conversation locks the hub has created.
func LockCount(s *Server) int {
	n := 0
	s.hub.locks.Range(func(_, _ any) bool { n++; return true })
	return n
}
