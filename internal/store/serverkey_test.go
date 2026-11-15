package store

import "testing"

// The server a store was filled from is recorded once and survives reopening.
func TestServerKeyIsRecordedAndReadBack(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ServerKey(); got != "" {
		t.Fatalf("ServerKey of a new store = %q, want empty", got)
	}
	if err := s.SetServerKey("SHA256:first"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerKey("SHA256:second"); err == nil {
		t.Fatal("SetServerKey replaced a recorded key")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, "acct")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.ServerKey(); got != "SHA256:first" {
		t.Fatalf("ServerKey after reopening = %q, want SHA256:first", got)
	}
}
