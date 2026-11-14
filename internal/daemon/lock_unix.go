//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

func platformSupported() error { return nil }

// lockFile takes an exclusive flock on path for the life of the process. The
// kernel drops it when the process exits, however it exits, so a stale lock
// cannot outlive its holder. The file itself is left in place: removing it
// would let a second process lock a fresh file while the first still runs.
func lockFile(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
