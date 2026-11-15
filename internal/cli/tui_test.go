package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/ipc"
)

// stubTUI replaces the interface runner and the terminal check, and reports
// how many times the interface was started.
func stubTUI(t *testing.T, terminal bool) *int {
	t.Helper()
	oldRun, oldTerm := runTUI, isTerminal
	n := 0
	runTUI = func(*cobra.Command) error { n++; return nil }
	isTerminal = func(io.Writer) bool { return terminal }
	t.Cleanup(func() { runTUI, isTerminal = oldRun, oldTerm })
	return &n
}

func TestNoArgumentsOnATerminalStartsTheTUI(t *testing.T) {
	n := stubTUI(t, true)
	out, _, err := run(t)
	if err != nil || *n != 1 || out != "" {
		t.Fatalf("err=%v starts=%d out=%q, want the interface started once", err, *n, out)
	}
}

func TestNoArgumentsWithoutATerminalPrintsHelp(t *testing.T) {
	n := stubTUI(t, false)
	out, _, err := run(t)
	if err != nil || *n != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("err=%v starts=%d out=%q, want help and no interface", err, *n, out)
	}
}

func TestTUICommand(t *testing.T) {
	n := stubTUI(t, false)
	if _, _, err := run(t, "tui"); err != nil || *n != 1 {
		t.Fatalf("termchat tui: err=%v starts=%d", err, *n)
	}
	if _, _, err := run(t, "tui", "extra"); ExitCode(err) != exitUsage {
		t.Fatalf("termchat tui extra: err=%v, want a usage error", err)
	}
	if _, _, err := run(t, "bogus"); ExitCode(err) != exitUsage || *n != 1 {
		t.Fatalf("unknown command: err=%v starts=%d", err, *n)
	}
}

func TestTUIReconnectDialNeverStartsTheProcess(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TERMCHAT_RUNTIME_DIR", dir)
	old := ipc.StartDaemon
	starts := 0
	ipc.StartDaemon = func(context.Context) error { starts++; return errors.New("not in tests") }
	t.Cleanup(func() { ipc.StartDaemon = old })

	dial, start := tuiDialers()
	if _, err := dial(context.Background()); !errors.Is(err, ipc.ErrNotRunning) || starts != 0 {
		t.Fatalf("reconnect dial: err=%v starts=%d, want ErrNotRunning and no start", err, starts)
	}
	if _, err := start(context.Background()); err == nil || starts != 1 {
		t.Fatalf("start dial: err=%v starts=%d, want one start", err, starts)
	}
}

func TestPasteGapFromConfig(t *testing.T) {
	for ms, want := range map[int]time.Duration{25: 25 * time.Millisecond, 80: 80 * time.Millisecond, 0: -1, -5: -1} {
		if got := pasteGap(ms); got != want {
			t.Errorf("pasteGap(%d) = %v, want %v", ms, got, want)
		}
	}
}
