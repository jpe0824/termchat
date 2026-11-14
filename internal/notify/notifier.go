package notify

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/sanitize"
)

const (
	// commandTimeout bounds every hook and operating-system command.
	commandTimeout = 5 * time.Second
	// maxRunning bounds the hooks, and separately the operating-system
	// commands, running at once; more are dropped rather than queued.
	maxRunning = 4
)

// Cmd is a command to run. Env entries are added to the inherited
// environment. Message text appears only as an argument or an Env value.
type Cmd struct {
	Name string
	Args []string
	Env  []string
}

// Runner runs c to completion, stopping it, and anything it started, when ctx
// ends.
type Runner func(ctx context.Context, c Cmd) error

// Notifier raises notifications through the operating system and the
// configured hook. It never blocks its caller: each command runs on its own
// goroutine, bounded in number and time.
type Notifier struct {
	hook string
	log  *slog.Logger

	// Injectable for tests.
	run      Runner
	lookPath func(string) (string, error)
	timeout  time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	hooks  chan struct{}
	oses   chan struct{}
}

// New returns a Notifier for the hook in cfg. A nil logger discards logs.
func New(cfg *config.Config, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Notifier{
		hook: cfg.Notify.Hook, log: logger,
		run: execRun, lookPath: exec.LookPath, timeout: commandTimeout,
		ctx: ctx, cancel: cancel,
		hooks: make(chan struct{}, maxRunning), oses: make(chan struct{}, maxRunning),
	}
}

// Wait returns when the running commands have ended, which is within their
// timeout.
func (n *Notifier) Wait() { n.wg.Wait() }

// Close kills the running commands and their process groups and returns once
// they are gone. The Notifier must not be used after.
func (n *Notifier) Close() {
	n.cancel()
	n.wg.Wait()
}

// OS posts n through the operating system's notification service.
func (n *Notifier) OS(p Notification) {
	c := platformCommand(n.lookPath, clean(p))
	if c == nil {
		return
	}
	n.start(n.oses, "operating-system notification", *c)
}

// Hook runs the configured hook for p, when there is one.
func (n *Notifier) Hook(p Notification) {
	if n.hook == "" {
		return
	}
	p = clean(p)
	n.start(n.hooks, "hook", hookCommand(n.hook, p))
}

// start runs c on a new goroutine unless sem is full, in which case the
// notification is dropped. Logs carry no message text.
func (n *Notifier) start(sem chan struct{}, what string, c Cmd) {
	select {
	case sem <- struct{}{}:
	default:
		n.log.Warn(what + " dropped: too many running")
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		defer func() { <-sem }()
		ctx, cancel := context.WithTimeout(n.ctx, n.timeout)
		defer cancel()
		if err := n.run(ctx, c); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				n.log.Warn(what+" failed", "err", "timed out")
				return
			}
			n.log.Warn(what+" failed", "err", err)
		}
	}()
}

// clean strips terminal and control characters from every field. Arguments
// and environment values cannot hold NUL, and a notification's fields are
// displayed or exported as plain text.
func clean(p Notification) Notification {
	p.ConvName = sanitize.Text(p.ConvName)
	p.Sender = sanitize.Text(p.Sender)
	p.Text = sanitize.Text(p.Text)
	return p
}
