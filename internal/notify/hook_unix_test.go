//go:build !windows

package notify

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hookRunner is a Notifier whose Hook always runs one hook command.
type hookRunner struct {
	*Notifier
	hook string
}

func (h hookRunner) Hook(p Notification) { h.Notifier.Hook(p, h.hook) }

func hookNotifier(t *testing.T, hook string) (hookRunner, *lockedBuf) {
	t.Helper()
	logs := &lockedBuf{}
	n := New(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(n.Close)
	return hookRunner{n, hook}, logs
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHookGetsEnvAndTimesOut(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	t.Setenv("OUT", out)
	t.Setenv("TERMCHAT_TEXT", "inherited value must be overridden")
	n, _ := hookNotifier(t, `printf '%s|%s|%s|%s|%s|%s' "$TERMCHAT_CONV" "$TERMCHAT_CONV_NAME" "$TERMCHAT_SENDER" "$TERMCHAT_TEXT" "$TERMCHAT_MENTION" "$TERMCHAT_DM" > "$OUT"; if read -r x; then printf open >> "$OUT"; else printf '|stdin-closed' >> "$OUT"; fi`)
	n.Hook(Notification{ConvID: "c9", ConvName: "ops", Sender: "Dana", Text: "hello", Mention: true})
	n.wait()
	if got, want := readFile(t, out), "c9|ops|Dana|hello|1|0|stdin-closed"; got != want {
		t.Errorf("hook saw %q, want %q", got, want)
	}

	n2, _ := hookNotifier(t, `printf '%s|%s' "$TERMCHAT_MENTION" "$TERMCHAT_DM" > "$OUT"`)
	n2.Hook(Notification{ConvID: "c1", DM: true})
	n2.wait()
	if got, want := readFile(t, out), "0|1"; got != want {
		t.Errorf("flags = %q, want %q", got, want)
	}
}

func TestHookTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	t.Setenv("PIDF", pidFile)
	n, logs := hookNotifier(t, `sleep 60 & echo $! > "$PIDF"; wait`)
	n.timeout = 300 * time.Millisecond
	start := time.Now()
	n.Hook(Notification{ConvID: "c", Text: "SECRET-TEXT"})
	n.wait()
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("hook ran %v, want it killed near the 300ms timeout", took)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(readFile(t, pidFile)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child %d of the hook is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "hook") {
		t.Errorf("timeout was not logged: %q", logs.String())
	}
	if strings.Contains(logs.String(), "SECRET-TEXT") {
		t.Errorf("message text reached the log: %q", logs.String())
	}
}

func TestHookFailureIsLoggedWithoutText(t *testing.T) {
	n, logs := hookNotifier(t, `exit 3`)
	n.Hook(Notification{Text: "SECRET-TEXT"})
	n.wait()
	if !strings.Contains(logs.String(), "hook failed") {
		t.Errorf("failure not logged: %q", logs.String())
	}
	if strings.Contains(logs.String(), "SECRET-TEXT") {
		t.Errorf("message text reached the log: %q", logs.String())
	}
}

func TestHookTextNotShellInterpreted(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	t.Setenv("OUT", out)
	for _, text := range []string{
		"$(touch " + filepath.Join(dir, "pwned") + ")",
		"`touch " + filepath.Join(dir, "pwned") + "`",
		`'; touch ` + filepath.Join(dir, "pwned") + `; '"`,
		"line1\nline2 $HOME \\ \"q\"",
	} {
		n, _ := hookNotifier(t, `printf %s "$TERMCHAT_TEXT" > "$OUT"`)
		n.Hook(Notification{Text: text, Sender: text, ConvName: text})
		n.wait()
		if got := readFile(t, out); got != text {
			t.Errorf("hook got %q, want %q byte for byte", got, text)
		}
		if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
			t.Fatalf("text %q ran as a command", text)
		}
	}
}

func TestHookConcurrencyIsBounded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIR", dir)
	n, logs := hookNotifier(t, `: > "$DIR/started.$$"; while [ ! -e "$DIR/go" ]; do sleep 0.02; done`)
	for range 4 {
		n.Hook(Notification{ConvID: "c"})
	}
	started := func() int {
		m, _ := filepath.Glob(filepath.Join(dir, "started.*"))
		return len(m)
	}
	deadline := time.Now().Add(5 * time.Second)
	for started() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d hooks started", started())
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range 3 {
		n.Hook(Notification{ConvID: "c"}) // over the limit: dropped, not queued
	}
	if !strings.Contains(logs.String(), "dropped") {
		t.Errorf("dropped hooks not logged: %q", logs.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n.wait()
	if got := started(); got != 4 {
		t.Errorf("%d hooks ran in total, want exactly 4 (the rest dropped)", got)
	}
}

func TestHookUnsetDoesNothing(t *testing.T) {
	n, logs := hookNotifier(t, "")
	n.Hook(Notification{Text: "x"})
	n.wait()
	if logs.String() != "" {
		t.Errorf("unexpected log: %q", logs.String())
	}
}

func TestCloseKillsRunningHooksPromptly(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	t.Setenv("PIDF", pidFile)
	n, _ := hookNotifier(t, `sleep 60 & echo $! > "$PIDF"; wait`)
	n.Hook(Notification{ConvID: "c"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	n.Close()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Close took %v with a hook sleeping for 60s", took)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(readFile(t, pidFile)))
	if err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(3 * time.Second); syscall.Kill(pid, 0) == nil; {
		if time.Now().After(end) {
			t.Fatalf("child %d of the hook survived Close", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
