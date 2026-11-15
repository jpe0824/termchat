// Package daemon is the background process: one per user, it owns the
// connector, the local store, the outbox and notifications, and serves the
// TUI and CLI over a Unix socket (package ipc).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// Options configures Run. Zero fields take the real implementations: the
// config file, the account's store under paths.Data(), the termchat
// connector, the system clock, a log file under paths.Data() and
// paths.Runtime(). A Store passed in is not closed by Run.
type Options struct {
	Connector connector.Connector
	Store     *store.Store
	Config    *config.Config
	// Notifier raises the OS and hook routes; the default does nothing.
	// Notification settings are read once, when the process starts.
	Notifier Notifier
	// NewNotifier builds the Notifier from the loaded config and the logger
	// when Notifier is nil. Run closes what it built, when it has a Close
	// method, after the account has stopped.
	NewNotifier func(*config.Config, *slog.Logger) Notifier
	Clock       Clock
	Logger      *slog.Logger
	RuntimeDir  string
	// Join registers with a server; the default is termchat.Join.
	Join func(ctx context.Context, req ipc.JoinReq) (config.Account, error)
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
	// problem is why the configured account is not up, "" when it is or
	// none is configured. setAside is where bring-up last moved a store.
	problem  string
	setAside string
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

	if opts.Notifier == nil && opts.NewNotifier != nil {
		d.notifier = opts.NewNotifier(d.cfg, d.log)
		if c, ok := d.notifier.(interface{ Close() }); ok {
			defer c.Close()
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
		// A failure is kept as the account's problem, which status shows; the
		// process stays up so a join can repair it.
		_, _ = d.bringUp(d.cfg.Accounts[0])
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

// bringUp opens the account's store and connector and starts its work. It
// is the one place that decides whether the store may be used: a store
// records the server key it was filled under, and a store filled under
// another key than the one now pinned is moved aside and replaced by a new
// one, because a rebuilt server numbers its events from 1 again under the
// same conversation ids. It returns the directory a store was moved to, if
// any. On failure the account stays down and the reason is its problem.
func (d *daemon) bringUp(acct config.Account) (string, error) {
	moved, err := d.open(acct)
	d.mu.Lock()
	if moved != "" {
		d.setAside = moved
	}
	d.problem = ""
	if err != nil {
		d.problem = err.Error()
	}
	d.mu.Unlock()
	if err != nil {
		d.log.Error("account not brought up", "account", acct.Name, "err", err)
	}
	if moved != "" {
		d.log.Info("set aside earlier history", "account", acct.Name, "dir", moved)
	}
	return moved, err
}

func (d *daemon) open(acct config.Account) (string, error) {
	st, own := d.opts.Store, false
	conn := d.opts.Connector
	var dataDir string
	if st == nil || conn == nil {
		var err error
		if dataDir, err = paths.Data(); err != nil {
			return "", err
		}
		if conn == nil {
			conn = termchat.New(acct, dataDir)
		}
	}
	pin := ""
	if k, ok := conn.(connector.ServerKeyer); ok {
		pin = k.ServerKey()
	}
	if st == nil {
		var err error
		if st, err = store.Open(dataDir, acct.Name); err != nil {
			return "", err
		}
		own = true
	}
	moved := ""
	if have := st.ServerKey(); pin != "" && have != "" && have != pin {
		if !own {
			return "", fmt.Errorf("the local store was filled under server key %s, not %s", have, pin)
		}
		if err := st.Close(); err != nil {
			return "", err
		}
		dir, err := d.moveAside(dataDir, acct.Name)
		if err != nil {
			return "", fmt.Errorf("set aside the earlier history: %w", err)
		}
		moved = dir
		if st, err = store.Open(dataDir, acct.Name); err != nil {
			return moved, err
		}
	}
	if pin != "" {
		if err := st.SetServerKey(pin); err != nil {
			if own {
				_ = st.Close()
			}
			return moved, err
		}
	}
	a := newAccount(d, acct.Name, conn, st, own)
	d.mu.Lock()
	d.acct = a
	d.mu.Unlock()
	a.start()
	return moved, nil
}

// account returns the running account, or why there is none.
func (d *daemon) account() (*account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.acct == nil && d.problem != "" {
		return nil, fmt.Errorf("account unavailable: %s", d.problem)
	}
	if d.acct == nil {
		return nil, ipc.ErrNoAccount
	}
	return d.acct, nil
}

// Stop implements ipc.Backend.
func (d *daemon) Stop() { d.stop() }

// Join implements ipc.Backend: it registers with the server, saves the
// account and brings it up without a restart. A configured account is
// replaced only when the join names its server and its connection is stuck in
// not_authorised or host_key_changed, which only a new join can leave, or when
// it could not be brought up. Whether the old store may be kept is decided by
// bring-up, from the server key the store recorded.
//
// The config is saved before the old account stops, so whatever fails later
// the process and the config name the same account, and a failed bring-up
// leaves it with a problem that another join may replace.
func (d *daemon) Join(ctx context.Context, req ipc.JoinReq) (ipc.JoinRes, error) {
	d.mu.Lock()
	old, err := d.replaceable(req.Server)
	if err != nil {
		d.mu.Unlock()
		return ipc.JoinRes{}, err
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
		join = func(ctx context.Context, req ipc.JoinReq) (config.Account, error) {
			dataDir, err := paths.Data()
			if err != nil {
				return config.Account{}, err
			}
			return termchat.Join(ctx, req.Server, req.Code, req.ServerKey, dataDir)
		}
	}
	acct, err := join(ctx, req)
	if err != nil {
		return ipc.JoinRes{}, err
	}
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return ipc.JoinRes{}, fmt.Errorf("%w: shutting down", ipc.ErrNotConnected)
	}
	prev := d.cfg.Accounts
	d.cfg.Accounts = []config.Account{acct}
	if err := d.cfg.Save(); err != nil {
		d.cfg.Accounts = prev
		d.mu.Unlock()
		return ipc.JoinRes{}, fmt.Errorf("save config: %w", err)
	}
	d.acct = nil
	d.mu.Unlock()
	if old != nil {
		old.shutdown()
		d.log.Info("replaced account", "account", old.name)
	}
	d.log.Info("joined server", "account", acct.Name)
	moved, err := d.bringUp(acct)
	return ipc.JoinRes{SetAside: moved}, err
}

// moveAside moves account's local store to <dataDir>/set-aside/<account>-<UTC
// time> and returns that directory, or "" when there was no store.
func (d *daemon) moveAside(dataDir, account string) (string, error) {
	src := filepath.Join(dataDir, "accounts", account)
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	parent := filepath.Join(dataDir, "set-aside")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(parent, account+"-"+d.clock.Now().UTC().Format("20060102-150405"))
	dst := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dst = fmt.Sprintf("%s-%d", base, i)
	}
	return dst, os.Rename(src, dst)
}

// replaceable reports whether a join to server may proceed, and returns the
// running account it would replace, if any. Callers hold d.mu.
func (d *daemon) replaceable(server string) (*account, error) {
	refuse := fmt.Errorf("%w: an account is already configured", ipc.ErrInvalid)
	if d.joining {
		return nil, refuse
	}
	if len(d.cfg.Accounts) == 0 && d.acct == nil {
		return nil, nil
	}
	if len(d.cfg.Accounts) != 1 || termchat.ServerAddr(d.cfg.Accounts[0].Server) != termchat.ServerAddr(server) {
		return nil, refuse
	}
	if d.acct == nil {
		if d.problem != "" {
			return nil, nil
		}
		return nil, refuse
	}
	switch d.acct.conn.State() {
	case connector.StateNotAuthorised, connector.StateHostKeyChanged:
		return d.acct, nil
	}
	return nil, refuse
}
