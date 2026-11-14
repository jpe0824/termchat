// Package daemon is the background process: one per user, it owns the
// connector, the local store, the outbox and notifications, and serves the
// TUI and CLI over a Unix socket (package ipc).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/connector/termchat"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/paths"
	"github.com/jpe0824/termchat/internal/store"
)

// ErrAlreadyRunning is returned by Run when another process holds the lock.
var ErrAlreadyRunning = errors.New("the termchat background process is already running")

// Clock is the time source for outbox expiry, backoff and throttling.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Notifier raises a notification outside the subscribed clients.
type Notifier interface{ Notify(ipc.NotifyPayload) }

type nopNotifier struct{}

func (nopNotifier) Notify(ipc.NotifyPayload) {}

// Options configures Run. Zero fields take the real implementations: the
// config file, the account's store under paths.Data(), the termchat
// connector, the system clock, a log file under paths.Data() and
// paths.Runtime(). A Store passed in is not closed by Run.
type Options struct {
	Connector  connector.Connector
	Store      *store.Store
	Config     *config.Config
	Notifier   Notifier
	Clock      Clock
	Logger     *slog.Logger
	RuntimeDir string
	// Join registers with a server; the default is termchat.Join.
	Join func(ctx context.Context, server, code string) (config.Account, error)
}

// daemon is the process-wide state; account holds what exists only once an
// account is configured.
type daemon struct {
	opts     Options
	clock    Clock
	log      *slog.Logger
	notifier Notifier
	srv      *ipc.Server
	stop     context.CancelFunc

	mu      sync.Mutex
	cfg     *config.Config
	acct    *account
	joining bool
	closing bool
}

// Run serves until ctx ends or a client asks it to stop, then shuts down
// completely: no goroutine, socket or lock outlives it.
func Run(ctx context.Context, opts Options) error {
	if err := platformSupported(); err != nil {
		return err
	}
	dir := opts.RuntimeDir
	if dir == "" {
		var err error
		if dir, err = paths.Runtime(); err != nil {
			return err
		}
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	sock := filepath.Join(dir, ipc.SocketName)
	if limit := maxSocketPath(); len(sock) > limit {
		return fmt.Errorf("socket path %s is %d bytes, over this system's limit of %d; set TERMCHAT_RUNTIME_DIR to a shorter directory",
			sock, len(sock), limit)
	}
	unlock, err := lockFile(filepath.Join(dir, ipc.LockName))
	if err != nil {
		return err
	}
	defer unlock()

	d := &daemon{opts: opts, clock: opts.Clock, log: opts.Logger, notifier: opts.Notifier, cfg: opts.Config}
	if d.clock == nil {
		d.clock = realClock{}
	}
	if d.notifier == nil {
		d.notifier = nopNotifier{}
	}
	if d.log == nil {
		logger, closeLog, err := openLog()
		if err != nil {
			return err
		}
		defer closeLog()
		d.log = logger
	}
	if d.cfg == nil {
		if d.cfg, err = config.Load(); err != nil {
			return err
		}
	}

	// The lock is ours, so a socket file here is a leftover.
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(sock) }()
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = ln.Close()
		return err
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	d.stop = stop
	d.srv = ipc.NewServer(d, d.log)
	if len(d.cfg.Accounts) > 0 {
		if err := d.bringUp(d.cfg.Accounts[0]); err != nil {
			_ = ln.Close()
			return err
		}
	}
	d.log.Info("background process started", "pid", os.Getpid())

	served := make(chan error, 1)
	go func() { served <- d.srv.Serve(ln) }()
	var serveErr error
	select {
	case <-runCtx.Done():
	case serveErr = <-served:
	}
	d.mu.Lock()
	d.closing = true
	d.mu.Unlock()
	d.srv.Close()
	if serveErr == nil {
		serveErr = <-served
	}
	d.mu.Lock()
	a := d.acct
	d.acct = nil
	d.mu.Unlock()
	if a != nil {
		a.shutdown()
	}
	d.log.Info("background process stopped")
	return serveErr
}

// privateDir makes sure dir exists and only its owner can enter it.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("runtime directory %s is accessible to other users and cannot be made private (%v); set TERMCHAT_RUNTIME_DIR", dir, err)
		}
	}
	return nil
}

// maxSocketPath is the longest socket path the platform's sockaddr_un holds,
// leaving room for the terminating NUL.
func maxSocketPath() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

func openLog() (*slog.Logger, func(), error) {
	dir, err := paths.Data()
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = f
	return slog.New(slog.NewTextHandler(w, nil)), func() { _ = f.Close() }, nil
}

// bringUp opens the account's store and connector and starts its work.
func (d *daemon) bringUp(acct config.Account) error {
	st, own := d.opts.Store, false
	conn := d.opts.Connector
	if st == nil || conn == nil {
		dataDir, err := paths.Data()
		if err != nil {
			return err
		}
		if st == nil {
			if st, err = store.Open(dataDir, acct.Name); err != nil {
				return err
			}
			own = true
		}
		if conn == nil {
			conn = termchat.New(acct, dataDir)
		}
	}
	a := newAccount(d, acct.Name, conn, st, own)
	d.mu.Lock()
	d.acct = a
	d.mu.Unlock()
	a.start()
	return nil
}

// account returns the running account or ErrNoAccount.
func (d *daemon) account() (*account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.acct == nil {
		return nil, ipc.ErrNoAccount
	}
	return d.acct, nil
}

// Stop implements ipc.Backend.
func (d *daemon) Stop() { d.stop() }

// Join implements ipc.Backend: it registers with the server, saves the
// account and brings it up without a restart.
func (d *daemon) Join(ctx context.Context, server, code string) error {
	d.mu.Lock()
	if d.acct != nil || d.joining || len(d.cfg.Accounts) > 0 {
		d.mu.Unlock()
		return fmt.Errorf("%w: an account is already configured", ipc.ErrInvalid)
	}
	d.joining = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.joining = false
		d.mu.Unlock()
	}()

	join := d.opts.Join
	if join == nil {
		join = func(ctx context.Context, server, code string) (config.Account, error) {
			dataDir, err := paths.Data()
			if err != nil {
				return config.Account{}, err
			}
			return termchat.Join(ctx, server, code, dataDir)
		}
	}
	acct, err := join(ctx, server, code)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return fmt.Errorf("%w: shutting down", ipc.ErrNotConnected)
	}
	d.cfg.Accounts = append(d.cfg.Accounts, acct)
	if err := d.cfg.Save(); err != nil {
		d.cfg.Accounts = d.cfg.Accounts[:len(d.cfg.Accounts)-1]
		d.mu.Unlock()
		return fmt.Errorf("save config: %w", err)
	}
	d.mu.Unlock()
	d.log.Info("joined server", "account", acct.Name)
	return d.bringUp(acct)
}
