package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const hostile = "$(touch x) `touch y` '\"; & | \\ -rf"

func TestDarwinCommand(t *testing.T) {
	n := Notification{Sender: hostile, Text: hostile, ConvName: "ops"}
	c := darwinCommand(n)
	if c.Name != "osascript" || len(c.Args) != 5 || c.Args[0] != "-e" || c.Args[2] != "--" {
		t.Fatalf("argv = %q %q", c.Name, c.Args)
	}
	if want := []string{hostile + " in #ops", hostile}; !reflect.DeepEqual(c.Args[3:], want) {
		t.Errorf("title and text = %q, want %q as separate arguments", c.Args[3:], want)
	}
	if strings.Contains(c.Args[1], "touch") || !strings.Contains(c.Args[1], "on run argv") {
		t.Errorf("script is not a fixed script over argv: %q", c.Args[1])
	}
	if len(c.Env) != 0 {
		t.Errorf("env = %q", c.Env)
	}
}

func TestLinuxCommand(t *testing.T) {
	n := Notification{Sender: hostile, Text: "it's a \\ test", DM: true}
	has := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, x := range names {
				if x == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	c := linuxCommand(has("notify-send", "gdbus"), n)
	want := &command{Name: "notify-send", Args: []string{"--app-name=termchat", "--", hostile, "it's a \\ test"}}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("notify-send command = %+v, want %+v", c, want)
	}

	c = linuxCommand(has("gdbus"), n)
	if c == nil || c.Name != "gdbus" {
		t.Fatalf("gdbus fallback = %+v", c)
	}
	wantArgs := []string{"call", "--session", "--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"'termchat'", "0", "''", wantGVariant(hostile), wantGVariant("it's a \\ test"), "[]", "{}", "5000"}
	if !reflect.DeepEqual(c.Args, wantArgs) {
		t.Errorf("gdbus args = %q\nwant      %q", c.Args, wantArgs)
	}

	if c := linuxCommand(has(), n); c != nil {
		t.Errorf("no notifier available: command = %+v, want none", c)
	}
}

func TestWindowsCommand(t *testing.T) {
	n := Notification{Sender: hostile, Text: hostile, ConvName: "ops"}
	c := windowsCommand(n)
	if c.Name != "powershell" || c.Args[len(c.Args)-2] != "-Command" {
		t.Fatalf("argv = %q %q", c.Name, c.Args)
	}
	for _, a := range c.Args {
		if strings.Contains(a, "touch") {
			t.Errorf("message text in argument %q", a)
		}
	}
	want := []string{"TERMCHAT_TITLE=" + hostile + " in #ops", "TERMCHAT_TEXT=" + hostile}
	if !reflect.DeepEqual(c.Env, want) {
		t.Errorf("env = %q, want %q", c.Env, want)
	}
}

// wait returns when the running commands have ended, which is within their
// timeout.
func (n *Notifier) wait() { n.wg.Wait() }

// recorder is an injected runner.
type recorder struct {
	mu       sync.Mutex
	cmds     []command
	deadline []time.Duration
	err      error
}

func (r *recorder) run(ctx context.Context, c command) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, c)
	if d, ok := ctx.Deadline(); ok {
		r.deadline = append(r.deadline, time.Until(d))
	}
	return r.err
}

func TestOSRouteRunsHostCommandOnce(t *testing.T) {
	logs := &lockedBuf{}
	n := New(slog.New(slog.NewTextHandler(logs, nil)))
	rec := &recorder{err: errors.New("boom")}
	n.run = rec.run
	n.lookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	n.OS(Notification{Sender: "dana", Text: hostile, DM: true})
	n.wait()
	if len(rec.cmds) != 1 {
		t.Fatalf("ran %d commands, want 1: %+v", len(rec.cmds), rec.cmds)
	}
	wantName := map[string]string{"darwin": "osascript", "linux": "notify-send", "windows": "powershell"}[runtime.GOOS]
	if wantName != "" && rec.cmds[0].Name != wantName {
		t.Errorf("command = %q, want %q", rec.cmds[0].Name, wantName)
	}
	if d := rec.deadline[0]; d <= 4*time.Second || d > 5*time.Second {
		t.Errorf("deadline in %v, want about 5s", d)
	}
	if !strings.Contains(logs.String(), "notification failed") {
		t.Errorf("failure not logged: %q", logs.String())
	}
	if strings.Contains(logs.String(), "touch") {
		t.Errorf("message text reached the log: %q", logs.String())
	}
}

func TestOSRouteNoNotifierIsSilent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux probes for a notifier")
	}
	n := New(nil)
	rec := &recorder{}
	n.run = rec.run
	n.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	n.OS(Notification{Text: "x"})
	n.wait()
	if len(rec.cmds) != 0 {
		t.Errorf("ran %+v with no notifier installed", rec.cmds)
	}
}

// wantGVariant is the expected GVariant text form of s.
func wantGVariant(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestLinuxBodyMarkupIsEscaped(t *testing.T) {
	n := Notification{Sender: "<b>dana</b> & co", Text: `a < b & <a href="x">c</a>`, DM: true}
	wantBody := `a &lt; b &amp; &lt;a href="x"&gt;c&lt;/a&gt;`
	has := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			if slices.Contains(names, name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}
	c := linuxCommand(has("notify-send"), n)
	want := []string{"--app-name=termchat", "--", "<b>dana</b> & co", wantBody}
	if c == nil || !reflect.DeepEqual(c.Args, want) {
		t.Errorf("notify-send args = %q, want %q (title plain, body escaped)", c.Args, want)
	}
	c = linuxCommand(has("gdbus"), n)
	if c == nil || c.Args[12] != wantGVariant(wantBody) || c.Args[11] != wantGVariant("<b>dana</b> & co") {
		t.Errorf("gdbus summary and body = %q, %q", c.Args[11], c.Args[12])
	}
}

func TestOSCommandIsBuiltOffTheCallingGoroutine(t *testing.T) {
	n := New(nil)
	n.run = (&recorder{}).run
	n.goos = "linux" // the platform that searches PATH
	release := make(chan struct{})
	n.lookPath = func(string) (string, error) {
		<-release // stands in for a slow PATH lookup
		return "", exec.ErrNotFound
	}
	returned := make(chan struct{})
	go func() {
		n.OS(Notification{Text: "x"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("OS blocked on building the command")
	}
	close(release)
	n.Close()
}

func TestPlatformCommandPicksTheOSRoute(t *testing.T) {
	found := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	for goos, want := range map[string]string{"darwin": "osascript", "linux": "notify-send", "windows": "powershell", "plan9": ""} {
		c := platformCommand(goos, found, Notification{Sender: "dana", Text: "hi", DM: true})
		got := ""
		if c != nil {
			got = c.Name
		}
		if got != want {
			t.Errorf("%s: command %q, want %q", goos, got, want)
		}
	}
}
