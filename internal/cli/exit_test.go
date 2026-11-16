package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

func TestExitCodeMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{ipc.ErrNotFound, 3},
		{ipc.ErrAmbiguous, 3},
		{ipc.ErrNoAccount, 4},
		{ipc.ErrNotConnected, 4},
		{ipc.ErrNotAuthorised, 4},
		{ipc.ErrForbidden, 4},
		{ipc.ErrTimeout, 5},
		{ipc.ErrSendDenied, 6},
		{ipc.ErrRefused, 7},
		{ipc.ErrShuttingDown, 1},
		{ipc.ErrInvalid, 1},
		{ipc.ErrClosed, 1},
		{errors.New("other"), 1},
	} {
		wrapped := fmt.Errorf("while doing it: %w", c.err)
		for _, err := range []error{c.err, wrapped} {
			if got := ExitCode(fail(err)); got != c.code {
				t.Errorf("ExitCode(fail(%q)) = %d, want %d", err, got, c.code)
			}
		}
	}
	if got := ExitCode(fail(usagef("bad"))); got != 2 {
		t.Errorf("usage error exits %d", got)
	}
	if fail(nil) != nil {
		t.Error("fail(nil) is not nil")
	}
}

func TestFailMessageIsOneSanitisedLine(t *testing.T) {
	err := fail(fmt.Errorf("%w: \x1b[2Jevil\nsecond\tline\x07", ipc.ErrNotFound))
	if got, want := err.Error(), "not found: evil second line"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// shuttingBackend answers Status with ErrShuttingDown a set number of times.
type shuttingBackend struct {
	ipc.Backend
	calls    atomic.Int32
	failures int32
}

func (b *shuttingBackend) Status(context.Context) (ipc.Status, error) {
	if b.calls.Add(1) <= b.failures {
		return ipc.Status{}, ipc.ErrShuttingDown
	}
	return ipc.Status{Account: "me"}, nil
}

func serveStub(t *testing.T, b ipc.Backend) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tcu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TERMCHAT_RUNTIME_DIR", dir)
	t.Setenv("TERMCHAT_CONFIG_DIR", filepath.Join(dir, "c"))
	ln, err := net.Listen("unix", filepath.Join(dir, ipc.SocketName))
	if err != nil {
		t.Skipf("no unix sockets: %v", err)
	}
	srv := ipc.NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Close)
	old := ipc.StartDaemon
	ipc.StartDaemon = func(context.Context) error { return errors.New("must not start") }
	t.Cleanup(func() { ipc.StartDaemon = old })
}

func TestCommandRetriesOnceWhenBackgroundProcessIsShuttingDown(t *testing.T) {
	b := &shuttingBackend{failures: 1}
	serveStub(t, b)
	root := Root()
	var out, errOut syncBuf
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if b.calls.Load() != 2 {
		t.Fatalf("Status called %d times, want 2 (one retry)", b.calls.Load())
	}
}

func TestCommandGivesUpAfterOneRetry(t *testing.T) {
	b := &shuttingBackend{failures: 99}
	serveStub(t, b)
	root := Root()
	var out, errOut syncBuf
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"status", "--json"})
	err := root.Execute()
	if err == nil || ExitCode(err) != 1 || err.Error() != ipc.ErrShuttingDown.Error() {
		t.Fatalf("err = %v (exit %d), want exit 1 with the shutting-down message", err, ExitCode(err))
	}
	if b.calls.Load() != 2 || out.String() != "" {
		t.Fatalf("Status called %d times, stdout %q; want 2 calls and no output", b.calls.Load(), out.String())
	}
}

func TestShutdownRetryDelayHonoursTimeout(t *testing.T) {
	serveStub(t, &shuttingBackend{failures: 99})
	root := Root()
	var out, errOut syncBuf
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"wait", "--timeout", "100ms", "--json"})
	start := time.Now()
	err := root.Execute()
	if d := time.Since(start); d > shutdownRetryDelay-100*time.Millisecond {
		t.Fatalf("returned after %s; the retry delay held up the timeout", d)
	}
	if ExitCode(err) != 5 || out.String() != "" {
		t.Fatalf("err = %v (exit %d), stdout %q; want exit 5 and no output", err, ExitCode(err), out.String())
	}
}
