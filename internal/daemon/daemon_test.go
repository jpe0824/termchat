package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/store"
)

func TestSingleInstance(t *testing.T) {
	e := newEnv(t)
	e.start()
	opts := e.options()
	opts.Store = nil
	err := Run(context.Background(), opts)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
	if _, err := e.c.Status(); err != nil {
		t.Fatalf("first daemon stopped answering: %v", err)
	}
}

func TestStaleSocketReplaced(t *testing.T) {
	e := newEnv(t)
	sock := filepath.Join(e.runDir, ipc.SocketName)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("leftover socket missing: %v", err)
	}
	c := e.start()
	if st, err := c.Status(); err != nil || st.Account != "me" {
		t.Fatalf("Status = %+v, %v", st, err)
	}
}

func TestSocketModeAndDirectory(t *testing.T) {
	e := newEnv(t)
	e.start()
	fi, err := os.Stat(filepath.Join(e.runDir, ipc.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want socket 0600", fi.Mode())
	}
	di, err := os.Stat(e.runDir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("runtime dir mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestSocketPathTooLong(t *testing.T) {
	e := newEnv(t)
	e.runDir = filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	if err := os.MkdirAll(e.runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := e.options()
	err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "TERMCHAT_RUNTIME_DIR") {
		t.Fatalf("Run = %v, want an error naming TERMCHAT_RUNTIME_DIR", err)
	}
}

func TestShutdownOnCancelReleasesEverything(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.stop()
	if _, err := os.Stat(filepath.Join(e.runDir, ipc.SocketName)); !os.IsNotExist(err) {
		t.Fatalf("socket still present after shutdown: %v", err)
	}
	// The lock was released: a new daemon starts.
	e.start()
	if _, err := e.c.Status(); err != nil {
		t.Fatal(err)
	}
}

func TestStopRequestShutsDown(t *testing.T) {
	e := newEnv(t)
	c := e.start()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-e.done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon still running after Stop")
	}
	e.cancel = nil
	if _, err := os.Stat(filepath.Join(e.runDir, ipc.SocketName)); !os.IsNotExist(err) {
		t.Fatalf("socket still present: %v", err)
	}
	_ = e.st.Close()
}

func TestConcurrentDialStartsOneDaemon(t *testing.T) {
	e := newEnv(t)
	st, err := store.Open(e.dataDir, "me")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e.st = st

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var starts, refused atomic.Int32
	var runErrs sync.Map
	saved := ipc.StartDaemon
	ipc.StartDaemon = func(context.Context) error {
		starts.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Run(ctx, e.options())
			if errors.Is(err, ErrAlreadyRunning) {
				refused.Add(1)
			} else if err != nil {
				runErrs.Store(err.Error(), true)
			}
		}()
		return nil
	}
	t.Cleanup(func() {
		ipc.StartDaemon = saved
		cancel()
		wg.Wait()
	})

	const n = 5
	clients := make([]*ipc.Client, n)
	errs := make([]error, n)
	var dials sync.WaitGroup
	for i := range n {
		dials.Add(1)
		go func() {
			defer dials.Done()
			clients[i], errs[i] = ipc.Dial(context.Background())
		}()
	}
	dials.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("dial %d: %v", i, errs[i])
		}
		defer clients[i].Close()
		if _, err := clients[i].Status(); err != nil {
			t.Fatalf("client %d Status: %v", i, err)
		}
	}
	if starts.Load() < 1 {
		t.Fatal("no daemon was started")
	}
	waitUntil(t, "losing daemons to exit", func() bool { return refused.Load() == starts.Load()-1 })
	runErrs.Range(func(k, _ any) bool {
		t.Errorf("Run failed: %v", k)
		return true
	})
}

func TestNoAccountServesOnlyStatusJoinStop(t *testing.T) {
	e := newEnv(t)
	e.cfg.Accounts = nil
	e.conn = nil
	c := e.start()
	st, err := c.Status()
	if err != nil || st.Account != "" || st.Conn != connector.StateOffline {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	if _, _, err := c.Chats(); !errors.Is(err, ipc.ErrNoAccount) {
		t.Fatalf("Chats = %v, want ErrNoAccount", err)
	}
	if _, err := c.Send("c", "hi", "tui"); !errors.Is(err, ipc.ErrNoAccount) {
		t.Fatalf("Send = %v, want ErrNoAccount", err)
	}
	if _, err := c.Resolve("#x"); !errors.Is(err, ipc.ErrNoAccount) {
		t.Fatalf("Resolve = %v, want ErrNoAccount", err)
	}
}

func TestJoinBringsDaemonUp(t *testing.T) {
	e := newEnv(t)
	e.cfg.Accounts = nil
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), posted("dana", "m1", "hello"))
	var gotServer, gotCode string
	e.adjust = func(o *Options) {
		o.Join = func(_ context.Context, req ipc.JoinReq) (config.Account, error) {
			gotServer, gotCode = req.Server, req.Code
			return config.Account{Name: "me", Server: req.Server, Handle: "me"}, nil
		}
	}
	c := e.start()
	if _, err := c.Join(ipc.JoinReq{Server: "chat.test", Code: "invite-1"}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if gotServer != "chat.test" || gotCode != "invite-1" {
		t.Fatalf("join called with %q %q", gotServer, gotCode)
	}
	if saved := e.file.saved(); len(saved.Accounts) != 1 || saved.Accounts[0].Name != "me" {
		t.Fatalf("saved config = %+v", saved)
	}
	waitUntil(t, "the joined account's conversations", func() bool {
		ids := chatIDs(t, c)
		return len(ids) == 1 && ids[0] == "r1"
	})
	st, err := c.Status()
	if err != nil || st.Account != "me" || st.Me.Handle != "me" || st.Conn != connector.StateConnected {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	if _, err := c.Send("r1", "hi", "tui"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the message to be sent", func() bool { return len(e.fake.Sent()) == 1 })
	if _, err := c.Join(ipc.JoinReq{Server: "chat.test", Code: "invite-2"}); !errors.Is(err, ipc.ErrInvalid) {
		t.Fatalf("second Join = %v, want ErrInvalid", err)
	}
}
