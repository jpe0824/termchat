package termchat

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DefaultPort is used when a server address names no port.
const DefaultPort = "7420"

// normalizeAddr returns server as host:port, adding DefaultPort when absent.
// ServerAddr is server as an account records it: host:port, with the default
// port added when none is given.
func ServerAddr(server string) string { return normalizeAddr(server) }

func normalizeAddr(server string) string {
	server = strings.TrimSpace(server)
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	host := strings.TrimSuffix(strings.TrimPrefix(server, "["), "]")
	return net.JoinHostPort(host, DefaultPort)
}

// fileSafe maps the characters that are not valid in a file name on some
// platform to underscores.
func fileSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:/\*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
}

func keyPath(dataDir, addr string) string {
	return filepath.Join(dataDir, "keys", fileSafe(addr)+".ed25519")
}

// loadKey reads the device key for addr. A missing file wraps fs.ErrNotExist.
func loadKey(dataDir, addr string) (ssh.Signer, error) {
	b, err := os.ReadFile(keyPath(dataDir, addr))
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("termchat: parse device key: %w", err)
	}
	return signer, nil
}

// loadOrCreateKey returns the device key for addr, generating it on first
// use. The file is created with mode 0600 in OpenSSH private key format.
func loadOrCreateKey(dataDir, addr string) (ssh.Signer, error) {
	signer, err := loadKey(dataDir, addr)
	if !errors.Is(err, fs.ErrNotExist) {
		return signer, err
	}
	path := keyPath(dataDir, addr)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("termchat: key directory: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return loadKey(dataDir, addr) // another Join created it first
	}
	if err != nil {
		return nil, fmt.Errorf("termchat: create device key: %w", err)
	}
	_, werr := f.Write(pem.EncodeToMemory(block))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return nil, fmt.Errorf("termchat: write device key: %w", werr)
	}
	return ssh.NewSignerFromKey(priv)
}
