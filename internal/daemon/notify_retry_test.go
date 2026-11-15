package daemon

import (
	"bytes"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// captureLogs makes the daemon log into the returned buffer.
func captureLogs(e *env) *lockedWriter {
	logs := &lockedWriter{w: &bytes.Buffer{}}
	prev := e.adjust
	e.adjust = func(o *Options) {
		if prev != nil {
			prev(o)
		}
		o.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	return logs
}

func (l *lockedWriter) count(substr string) int { return strings.Count(l.String(), substr) }

func waitLogged(t *testing.T, logs *lockedWriter, substr string, n int) {
	t.Helper()
	waitUntil(t, fmt.Sprintf("%d log lines with %q", n, substr), func() bool { return logs.count(substr) >= n })
}

// pushUnknown delivers a message for a conversation the store does not hold.
func pushUnknown(e *env, conv, msgID, text string, seq int64) {
	push(e, conv, "dana", msgID, text, seq)
}

// appear upserts conv in the daemon's store, as the server's conversation
// update does, and waits until it is listed.
func appear(t *testing.T, e *env, c *ipc.Client, conv model.Conversation) {
	t.Helper()
	e.fake.Inject(connector.Update{Conv: &conv})
	waitUntil(t, "conversation listed", func() bool { return slices.Contains(chatIDs(t, c), conv.ID) })
}

func TestMessageBeforeItsConversationNotifiesOnRetry(t *testing.T) {
	e, rec := noteEnv(t)
	logs := captureLogs(e)
	c := started(t, e)
	pushUnknown(e, "d9", "m1", "hello from a new dm", 1)
	waitLogged(t, logs, "cannot evaluate", 1)
	if got := rec.osCalls(); len(got) != 0 {
		t.Fatalf("notified before the conversation was known: %+v", got)
	}
	appear(t, e, c, dm("d9", "dana"))
	e.clock.Advance(time.Second)
	waitUntil(t, "retry notification", func() bool { return len(rec.osCalls()) == 1 })
	want := ipc.NotifyPayload{ConvID: "d9", ConvName: "dana", Sender: "Dana", Text: "hello from a new dm", DM: true}
	if got := rec.osCalls()[0]; got != want {
		t.Fatalf("payload = %+v, want %+v", got, want)
	}
	e.clock.Advance(5 * time.Second)
	e.clock.Advance(30 * time.Second)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 1 {
		t.Fatalf("notified %d times, want once: %+v", len(got), got)
	}
}

func TestUndecidableMessageIsGivenUpOnAfterThreeRetries(t *testing.T) {
	e, rec := noteEnv(t)
	logs := captureLogs(e)
	started(t, e)
	pushUnknown(e, "d9", "m1", "SECRET-TEXT", 1)
	waitLogged(t, logs, "cannot evaluate", 1)
	e.clock.Advance(time.Second)
	waitLogged(t, logs, "cannot evaluate", 2) // first retry failed
	e.clock.Advance(5 * time.Second)
	waitLogged(t, logs, "cannot evaluate", 3) // second retry failed
	e.clock.Advance(30 * time.Second)
	waitLogged(t, logs, "gave up", 1) // third retry failed
	e.clock.Advance(time.Hour)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("notified %+v", got)
	}
	if n := logs.count("gave up"); n != 1 {
		t.Errorf("%d gave-up lines, want 1", n)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "gave up") && !strings.Contains(line, "d9") {
			t.Errorf("gave-up line lacks the conversation id: %q", line)
		}
	}
	if strings.Contains(logs.String(), "SECRET-TEXT") {
		t.Error("message text reached the log")
	}
}

func TestMessageReadBeforeTheRetryIsNotNotified(t *testing.T) {
	e, rec := noteEnv(t)
	logs := captureLogs(e)
	c := started(t, e)
	pushUnknown(e, "d9", "m1", "read elsewhere", 1)
	waitLogged(t, logs, "cannot evaluate", 1)
	appear(t, e, c, dm("d9", "dana"))
	e.fake.Inject(connector.Update{Read: &connector.ReadUpdate{ConvID: "d9", Seq: 1}})
	waitUntil(t, "read position", func() bool {
		mine, _, _ := c.Chats()
		for _, v := range mine {
			if v.ID == "d9" && v.ReadSeq == 1 {
				return true
			}
		}
		return false
	})
	e.clock.Advance(time.Second)
	e.clock.Advance(5 * time.Second)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Fatalf("notified %+v for a message already read", got)
	}
}

func TestPendingRetriesAreBounded(t *testing.T) {
	e, rec := noteEnv(t)
	logs := captureLogs(e)
	c := started(t, e)
	for i := 1; i <= 70; i++ {
		pushUnknown(e, "d9", fmt.Sprintf("m%d", i), fmt.Sprintf("message %d", i), int64(i))
	}
	waitLogged(t, logs, "cannot evaluate", 70)
	waitLogged(t, logs, "dropped pending", 6)
	if n := logs.count("dropped pending"); n != 6 {
		t.Fatalf("%d dropped lines, want 6", n)
	}
	appear(t, e, c, dm("d9", "dana"))
	e.clock.Advance(time.Second)
	waitUntil(t, "64 retries", func() bool { return len(rec.osCalls()) == 64 })
	sentinel(t, e, rec)
	got := dropSentinels(rec.osCalls())
	if len(got) != 64 || slices.Contains(texts(got), "message 1") || !slices.Contains(texts(got), "message 70") {
		t.Fatalf("%d notified; the oldest six should have been dropped, the newest kept", len(got))
	}
}

func goroutinesIn(fn string) int {
	buf := make([]byte, 1<<20)
	return strings.Count(string(buf[:runtime.Stack(buf, true)]), fn)
}

func TestShutdownWithPendingRetriesLeavesNothingRunning(t *testing.T) {
	e, _ := noteEnv(t)
	logs := captureLogs(e)
	started(t, e)
	pushUnknown(e, "d9", "m1", "x", 1)
	waitLogged(t, logs, "cannot evaluate", 1)
	if goroutinesIn("daemon.(*notifications).run") == 0 {
		t.Fatal("no retry worker while a retry is pending")
	}
	e.stop()
	if n := goroutinesIn("daemon.(*notifications).run"); n != 0 {
		t.Fatalf("%d retry workers still running after shutdown", n)
	}
}
