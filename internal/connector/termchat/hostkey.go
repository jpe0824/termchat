package termchat

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyChanged means the server presented a host key other than the one
// pinned for it. The pin is never replaced automatically.
var ErrHostKeyChanged = errors.New("termchat: server host key does not match the pinned key")

// ErrHostKeyUnpinned means no host key is pinned for the server yet.
var ErrHostKeyUnpinned = errors.New("termchat: server host key is not pinned")

const knownServersFile = "known_servers"

// pinMu serialises writers of known_servers within this process.
var pinMu sync.Mutex

// pinnedKey returns the host key pinned for addr, or ErrHostKeyUnpinned.
// Each line of known_servers is "host:port" then the authorized-key form of
// the host key.
func pinnedKey(dataDir, addr string) (ssh.PublicKey, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, knownServersFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrHostKeyUnpinned
	}
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		host, rest, ok := strings.Cut(line, " ")
		if line == "" || line[0] == '#' || !ok || host != addr {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rest))
		if err != nil {
			return nil, fmt.Errorf("termchat: bad %s entry for %s: %w", knownServersFile, addr, err)
		}
		return key, nil
	}
	return nil, ErrHostKeyUnpinned
}

// pinKey records key as the host key of addr. It succeeds when the same key
// is already pinned and returns ErrHostKeyChanged when a different one is.
func pinKey(dataDir, addr string, key ssh.PublicKey) error {
	pinMu.Lock()
	defer pinMu.Unlock()
	switch have, err := pinnedKey(dataDir, addr); {
	case err == nil:
		if bytes.Equal(have.Marshal(), key.Marshal()) {
			return nil
		}
		return ErrHostKeyChanged
	case !errors.Is(err, ErrHostKeyUnpinned):
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, knownServersFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "%s %s\n", addr, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// hostKeyCheck records how a host key callback decided, because the ssh
// package wraps callback errors beyond reliable matching.
type hostKeyCheck struct {
	mu        sync.Mutex
	err       error
	presented ssh.PublicKey
}

// callback accepts the key pinned for addr. With allowUnpinned it also
// accepts any key when none is pinned, and the caller then pins it.
func (h *hostKeyCheck) callback(dataDir, addr string, allowUnpinned bool) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		pinned, err := pinnedKey(dataDir, addr)
		switch {
		case errors.Is(err, ErrHostKeyUnpinned) && allowUnpinned:
			err = nil
		case err == nil && !bytes.Equal(pinned.Marshal(), key.Marshal()):
			err = ErrHostKeyChanged
		}
		h.mu.Lock()
		h.err, h.presented = err, key
		h.mu.Unlock()
		return err
	}
}

func (h *hostKeyCheck) result() (ssh.PublicKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.presented, h.err
}
