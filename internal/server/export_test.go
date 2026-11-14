package server

import "time"

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
