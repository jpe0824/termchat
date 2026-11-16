package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/connector/fake"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

func TestMain(m *testing.M) {
	// Never execute the test binary as a daemon; tests start daemons in
	// process and Dial only waits for them.
	ipc.StartDaemon = func(context.Context) error { return nil }
	os.Exit(m.Run())
}

var me = model.Person{Handle: "me", DisplayName: "Me"}

// fakeClock is a manual clock. After registers a waiter that Advance fires.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now().UTC().Truncate(time.Millisecond)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

// pending counts registered waiters.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// tick waits until something waits on the clock, then advances it by d.
func (c *fakeClock) tick(t *testing.T, d time.Duration) {
	t.Helper()
	waitUntil(t, "a clock waiter", func() bool { return c.pending() > 0 })
	c.Advance(d)
}

// shortDir returns a directory whose socket path fits the platform limit;
// the default temp directory on macOS is too deep.
func shortDir(t *testing.T) string {
	t.Helper()
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "tcd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// env is one daemon under test with a fake connector, a temp store and a
// fake clock.
type env struct {
	t       *testing.T
	fake    *fake.Fake
	conn    connector.Connector // what the daemon is given; fake unless wrapped
	clock   *fakeClock
	cfg     *config.Config
	dataDir string
	runDir  string
	st      *store.Store
	// file is config.toml, seeded from cfg when the daemon starts.
	file   *cfgFile
	adjust func(*Options)
	// built receives the daemon Run builds.
	built chan *daemon
	d     *daemon
	// mu guards file and built, which options sets on first use.
	mu sync.Mutex

	cancel context.CancelFunc
	done   chan error
	c      *ipc.Client
}

func testConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	cfg.Accounts = []config.Account{{Name: "me", Server: "chat.test:7420", Handle: me.Handle}}
	return cfg
}

// newEnv prepares directories and the fake; call start to run the daemon.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, fake: fake.New(me), clock: newFakeClock(), dataDir: t.TempDir(), runDir: shortDir(t)}
	e.conn = e.fake
	t.Setenv("TERMCHAT_CONFIG_DIR", t.TempDir())
	t.Setenv("TERMCHAT_DATA_DIR", e.dataDir)
	t.Setenv("TERMCHAT_RUNTIME_DIR", e.runDir)
	e.cfg = testConfig()
	return e
}

func (e *env) options() Options {
	opts := Options{
		Connector:  e.conn,
		Store:      e.st,
		Config:     e.cfg,
		Clock:      e.clock,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		RuntimeDir: e.runDir,
	}
	e.mu.Lock()
	if e.file == nil {
		e.file = newCfgFile(e.t, e.cfg)
	}
	if e.built == nil {
		e.built = make(chan *daemon, 1)
	}
	file, built := e.file, e.built
	e.mu.Unlock()
	opts.LoadConfig, opts.SaveConfig = file.load, file.save
	// Only the daemon that won the lock is built; the send must still never
	// block Run.
	opts.built = func(d *daemon) {
		select {
		case built <- d:
		default:
		}
	}
	if e.adjust != nil {
		e.adjust(&opts)
	}
	return opts
}

// start runs the daemon and connects a client to it.
func (e *env) start() *ipc.Client {
	e.t.Helper()
	if e.st == nil && len(e.cfg.Accounts) > 0 {
		st, err := store.Open(e.dataDir, "me")
		if err != nil {
			e.t.Fatal(err)
		}
		e.st = st
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan error, 1)
	opts := e.options()
	go func() { e.done <- Run(ctx, opts) }()
	c, err := ipc.Dial(context.Background())
	if err != nil {
		cancel()
		e.t.Fatalf("dial: %v (run: %v)", err, e.runErr())
	}
	e.c = c
	e.t.Cleanup(e.stop)
	return c
}

// daemon returns the daemon the running Run built.
func (e *env) daemon() *daemon {
	e.t.Helper()
	if e.d == nil {
		select {
		case e.d = <-e.built:
		case <-time.After(5 * time.Second):
			e.t.Fatal("Run built no daemon")
		}
	}
	return e.d
}

func (e *env) runErr() error {
	select {
	case err := <-e.done:
		return err
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

// stop shuts the daemon down and closes the test's store.
func (e *env) stop() {
	if e.cancel == nil {
		return
	}
	if e.c != nil {
		_ = e.c.Close()
	}
	e.cancel()
	select {
	case err := <-e.done:
		if err != nil {
			e.t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		e.t.Error("Run did not return after cancel")
	}
	e.cancel, e.d, e.built = nil, nil, nil
	if e.st != nil {
		_ = e.st.Close()
		e.st = nil
	}
}

// subscribe returns a channel of events for the rest of the test.
func (e *env) subscribe() <-chan ipc.Event {
	e.t.Helper()
	c, err := ipc.Dial(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	ch, err := c.Subscribe(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return ch
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitEvent reads events until match accepts one.
func waitEvent(t *testing.T, ch <-chan ipc.Event, what string, match func(ipc.Event) bool) ipc.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event stream closed waiting for %s", what)
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// chatIDs lists the ids of the user's conversations.
func chatIDs(t *testing.T, c *ipc.Client) []string {
	t.Helper()
	mine, _, err := c.Chats()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range mine {
		ids = append(ids, v.ID)
	}
	return ids
}

// posted builds a message event from author with text.
func posted(author, msgID, text string) model.Event {
	return model.Event{Kind: model.MessagePosted, Actor: author, MsgID: msgID,
		Body: &model.Body{Format: "text/plain", Data: []byte(text)}}
}

func room(id, name string, members ...string) model.Conversation {
	return model.Conversation{ID: id, Kind: model.ConvRoom, Name: name, Members: members}
}
