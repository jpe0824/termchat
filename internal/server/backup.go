package server

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

const (
	maxDBEntry      = 64 << 30
	maxHostKeyEntry = 64 << 10
)

var (
	// ErrEmptyPassphrase is returned for a blank backup passphrase.
	ErrEmptyPassphrase = errors.New("passphrase must not be empty")
	// ErrWrongPassphrase is returned when a backup cannot be decrypted.
	ErrWrongPassphrase = errors.New("wrong passphrase, or the file is not a termchat backup")
	// ErrBadBackup is returned when a decrypted backup holds anything but the
	// database and host key.
	ErrBadBackup = errors.New("backup contents are not valid")
)

// Snapshot writes a consistent copy of the database to path, which must not
// exist. It is safe while the server is running.
func (s *Store) Snapshot(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// Backup writes dataDir's database snapshot and host key, as a tar stream
// encrypted with passphrase, to out. The host key is included because
// clients pin it. out is created with mode 0600 and must not exist; it is
// removed again if the backup fails. The temporary snapshot is always removed.
func Backup(dataDir, out, passphrase string) (err error) {
	if passphrase == "" {
		return ErrEmptyPassphrase
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dataDir, dbFile)); err != nil {
		return fmt.Errorf("no database in %s: %w", dataDir, err)
	}
	// A server that has never served has no host key yet; creating it now
	// fixes the identity the backup preserves.
	hostKeyPath := filepath.Join(dataDir, hostKeyFile)
	if _, err := loadOrCreateHostKey(hostKeyPath); err != nil {
		return err
	}
	hostKey, err := os.ReadFile(hostKeyPath)
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	f, err := createPrivate(out)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; refusing to overwrite", out)
		}
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out)
		}
	}()

	tmp, err := os.MkdirTemp(dataDir, "backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, dbFile)
	st, err := OpenStore(dataDir)
	if err != nil {
		return err
	}
	err = st.Snapshot(snap)
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	enc, err := age.Encrypt(f, recipient)
	if err != nil {
		return err
	}
	db, err := os.Open(snap)
	if err != nil {
		return err
	}
	defer db.Close()
	fi, err := db.Stat()
	if err != nil {
		return err
	}
	tw := tar.NewWriter(enc)
	if err := tarEntry(tw, dbFile, fi.Size(), db); err != nil {
		return err
	}
	if err := tarEntry(tw, hostKeyFile, int64(len(hostKey)), bytes.NewReader(hostKey)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return enc.Close()
}

// createPrivate creates path with mode 0600, failing if it already exists.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func tarEntry(tw *tar.Writer, name string, size int64, r io.Reader) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := io.Copy(tw, r)
	return err
}

// Restore decrypts the backup in file into dataDir, which must be missing or
// empty. Only server.db and host_key are accepted. If extraction fails after
// the directory exists, it is left empty.
func Restore(file, dataDir, passphrase string) (err error) {
	if passphrase == "" {
		return ErrEmptyPassphrase
	}
	if entries, rerr := os.ReadDir(dataDir); rerr == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty; refusing to restore into it", dataDir)
	} else if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return rerr
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	dec, err := age.Decrypt(in, identity)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return ErrWrongPassphrase
		}
		return fmt.Errorf("%w: %v", ErrWrongPassphrase, err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			for _, name := range []string{dbFile, hostKeyFile} {
				os.Remove(filepath.Join(dataDir, name))
			}
		}
	}()

	tr := tar.NewReader(dec)
	seen := map[string]bool{}
	for {
		hdr, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			return fmt.Errorf("%w: %v", ErrBadBackup, nerr)
		}
		limit, ok := map[string]int64{dbFile: maxDBEntry, hostKeyFile: maxHostKeyEntry}[hdr.Name]
		switch {
		case !ok:
			return fmt.Errorf("%w: unexpected entry %q", ErrBadBackup, hdr.Name)
		case hdr.Typeflag != tar.TypeReg:
			return fmt.Errorf("%w: %q is not a regular file", ErrBadBackup, hdr.Name)
		case hdr.Size < 0 || hdr.Size > limit:
			return fmt.Errorf("%w: %q is too large", ErrBadBackup, hdr.Name)
		case seen[hdr.Name]:
			return fmt.Errorf("%w: duplicate entry %q", ErrBadBackup, hdr.Name)
		}
		seen[hdr.Name] = true
		if err := extract(filepath.Join(dataDir, hdr.Name), tr); err != nil {
			return err
		}
	}
	// Reading to the end makes age verify that the stream was not truncated.
	if _, err := io.Copy(io.Discard, dec); err != nil {
		return fmt.Errorf("%w: %v", ErrBadBackup, err)
	}
	for _, name := range []string{dbFile, hostKeyFile} {
		if !seen[name] {
			return fmt.Errorf("%w: missing %s", ErrBadBackup, name)
		}
	}
	return nil
}

func extract(path string, r io.Reader) error {
	f, err := createPrivate(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
