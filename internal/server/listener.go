package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/proto"
)

// Pinned limits; a zero Limits field means the default.
const (
	defaultFramesPerSec    = 20
	defaultBurst           = 40
	defaultAuthFailsPerMin = 5
	defaultMaxBody         = 8192

	authFailWindow   = time.Minute
	handshakeTimeout = 10 * time.Second
	keyRecheck       = 2 * time.Second
	writeTimeout     = 10 * time.Second
	channelType      = "termchat"
	hostKeyFile      = "host_key"
)

// Limits bound what one connection or address may do. Zero fields take the
// defaults: 20 frames/s with burst 40, 5 failed authentications per address
// per minute, and 8192-byte message bodies.
type Limits struct {
	FramesPerSec, Burst, AuthFailsPerMin, MaxBody int
}

func (l Limits) withDefaults() Limits {
	if l.FramesPerSec <= 0 {
		l.FramesPerSec = defaultFramesPerSec
	}
	if l.Burst <= 0 {
		l.Burst = defaultBurst
	}
	if l.AuthFailsPerMin <= 0 {
		l.AuthFailsPerMin = defaultAuthFailsPerMin
	}
	if l.MaxBody <= 0 {
		l.MaxBody = defaultMaxBody
	}
	return l
}

// Options configures New. A nil Logger discards all log output.
type Options struct {
	Addr, DataDir string
	Limits        Limits
	Logger        *slog.Logger
}

// HandlerFunc serves one request frame. A nil *proto.Error with a nil result
// answers with an empty payload.
type HandlerFunc func(ctx context.Context, s *Session, f proto.Frame) (any, *proto.Error)

// Server accepts SSH connections and serves the termchat frame protocol on
// the one channel each connection may open.
type Server struct {
	limits  Limits
	log     *slog.Logger
	store   *Store
	ln      net.Listener
	cfg     *ssh.ServerConfig
	hostKey ssh.PublicKey

	wg        sync.WaitGroup
	closeOnce sync.Once

	mu        sync.Mutex
	handlers  map[string]HandlerFunc
	conns     map[net.Conn]struct{}
	sessions  map[*Session]struct{}
	closed    bool
	now       func() time.Time
	recheck   time.Duration
	handshake time.Duration
	fails     map[string][]time.Time
}

// New opens the store in opts.DataDir, loads or creates the host key there,
// and binds opts.Addr. The listener accepts nothing until Serve runs.
func New(opts Options) (*Server, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("server: data dir: %w", err)
	}
	signer, err := loadOrCreateHostKey(filepath.Join(opts.DataDir, hostKeyFile))
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("server: listen: %w", err)
	}
	s := &Server{
		limits:    opts.Limits.withDefaults(),
		log:       log,
		store:     store,
		ln:        ln,
		hostKey:   signer.PublicKey(),
		handlers:  map[string]HandlerFunc{},
		conns:     map[net.Conn]struct{}{},
		sessions:  map[*Session]struct{}{},
		now:       time.Now,
		recheck:   keyRecheck,
		handshake: handshakeTimeout,
		fails:     map[string][]time.Time{},
	}
	s.cfg = &ssh.ServerConfig{PublicKeyCallback: publicKeyCallback}
	s.cfg.AddHostKey(signer)
	s.Handle(proto.TypeHello, s.handleHello)
	s.Handle(proto.TypeInviteRedeem, s.handleRedeem)
	return s, nil
}

// publicKeyCallback admits every key. Whether the key belongs to a person is
// decided per session, so that an unknown key can still redeem an invite.
func publicKeyCallback(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	return &ssh.Permissions{Extensions: map[string]string{
		"fingerprint": ssh.FingerprintSHA256(key),
		"key":         base64.StdEncoding.EncodeToString(key.Marshal()),
	}}, nil
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	pemBytes, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		block, gerr := ssh.MarshalPrivateKey(priv, "")
		if gerr != nil {
			return nil, gerr
		}
		pemBytes = pem.EncodeToMemory(block)
		f, gerr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if gerr != nil {
			return nil, fmt.Errorf("server: create host key: %w", gerr)
		}
		_, gerr = f.Write(pemBytes)
		if cerr := f.Close(); gerr == nil {
			gerr = cerr
		}
		if gerr != nil {
			return nil, fmt.Errorf("server: write host key: %w", gerr)
		}
	} else if err != nil {
		return nil, fmt.Errorf("server: read host key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("server: parse host key: %w", err)
	}
	return signer, nil
}

// Addr is the bound listen address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// HostKey is the server's public host key.
func (s *Server) HostKey() ssh.PublicKey { return s.hostKey }

// Store is the store opened by New. It stays open until Serve returns or
// Close is called.
func (s *Server) Store() *Store { return s.store }

// Handle registers the handler for a request type, replacing any earlier one.
func (s *Server) Handle(typ string, h HandlerFunc) {
	s.mu.Lock()
	s.handlers[typ] = h
	s.mu.Unlock()
}

func (s *Server) handler(typ string) HandlerFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handlers[typ]
}

// Sessions returns the live known sessions of a person.
func (s *Server) Sessions(handle string) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Session
	for sess := range s.sessions {
		if p, known := sess.identity(); known && p.Handle == handle {
			out = append(out, sess)
		}
	}
	return out
}

// Kick closes every session authenticated with the key fingerprint.
func (s *Server) Kick(fingerprint string) {
	s.mu.Lock()
	var hit []*Session
	for sess := range s.sessions {
		if sess.Fingerprint == fingerprint {
			hit = append(hit, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range hit {
		sess.Close()
	}
}

// Serve accepts connections until ctx is cancelled, then closes the listener,
// every session and the store, and returns nil.
func (s *Server) Serve(ctx context.Context) error {
	stop := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		select {
		case <-ctx.Done():
			s.ln.Close()
		case <-stop:
		}
	}()
	s.acceptLoop(ctx)
	close(stop)
	watcher.Wait()
	s.Close()
	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.log.Warn("accept failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		if s.addressBlocked(remoteIP(conn.RemoteAddr())) {
			conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveConn(conn)
	}
}

// Close stops accepting, closes every connection, waits for their goroutines
// and closes the store. It is safe to call more than once.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		s.ln.Close()
		for _, c := range conns {
			c.Close()
		}
		s.wg.Wait()
		err = s.store.Close()
	})
	return err
}

func (s *Server) serveConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	s.mu.Lock()
	hs := s.handshake
	s.mu.Unlock()
	// The deadline covers the handshake and the wait for the channel, so a
	// silent client cannot hold the connection.
	conn.SetDeadline(time.Now().Add(hs))
	sconn, chans, reqs, err := ssh.NewServerConn(conn, s.cfg)
	if err != nil {
		s.log.Debug("handshake failed", "remote", conn.RemoteAddr().String(), "err", err)
		return
	}
	defer sconn.Close()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ssh.DiscardRequests(reqs)
	}()

	started := false
	for nc := range chans {
		if nc.ChannelType() != channelType || started {
			nc.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		sess, err := s.newSession(conn, sconn)
		if err != nil {
			s.log.Error("session setup failed", "err", err)
			nc.Reject(ssh.ConnectionFailed, "unavailable")
			return
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		started = true
		conn.SetDeadline(time.Time{})
		s.wg.Add(2)
		go func() {
			defer s.wg.Done()
			ssh.DiscardRequests(chReqs)
		}()
		go func() {
			defer s.wg.Done()
			sess.run(ch)
		}()
	}
}

func remoteIP(a net.Addr) string {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return host
}

// addressBlocked reports whether ip has more failures in the window than
// AuthFailsPerMin allows.
func (s *Server) addressBlocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pruneFails(ip)) > s.limits.AuthFailsPerMin
}

// recordAuthFailure notes a failed authentication from ip and reports whether
// the address is now over the cap.
func (s *Server) recordAuthFailure(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.fails) > 256 {
		for other := range s.fails {
			s.pruneFails(other)
		}
	}
	times := append(s.pruneFails(ip), s.now())
	// Only the newest cap+1 entries can affect the verdict.
	if extra := len(times) - (s.limits.AuthFailsPerMin + 1); extra > 0 {
		times = times[extra:]
	}
	s.fails[ip] = times
	return len(times) > s.limits.AuthFailsPerMin
}

// pruneFails drops entries older than the window. Callers hold s.mu.
func (s *Server) pruneFails(ip string) []time.Time {
	now := s.now()
	times := s.fails[ip]
	keep := times[:0]
	for _, t := range times {
		if now.Sub(t) < authFailWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(s.fails, ip)
		return nil
	}
	s.fails[ip] = keep
	return keep
}
