package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

func requireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s mode %o, want %o", filepath.Base(path), got, want)
	}
}

// The data directory may be one the operator made with a default umask; the
// database and its WAL and shared-memory files must still be private.
func TestDatabaseFilesArePrivateInExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreatePerson(model.Person{Handle: "ann"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{dbFile, dbFile + "-wal", dbFile + "-shm"} {
		requireMode(t, filepath.Join(dir, name), 0o600)
	}
}

func TestExistingDatabaseIsMadePrivate(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	path := DBPath(dir)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	requireMode(t, path, 0o600)
}
