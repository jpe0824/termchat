package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
