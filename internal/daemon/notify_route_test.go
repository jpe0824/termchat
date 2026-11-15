package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
)

// uiClient attaches a client that declared itself a UI.
func uiClient(t *testing.T, conv string, focused bool) <-chan ipc.Event {
	return uiClientCan(t, true, conv, focused)
}

// uiClientCan is uiClient with the capability to show terminal notifications
// declared or not.
func uiClientCan(t *testing.T, canNotify bool, conv string, focused bool) <-chan ipc.Event {
	t.Helper()
	c, err := ipc.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	ch, err := c.SubscribeUI(ctx, canNotify)
	if err != nil {
		t.Fatal(err)
	}
	if conv != "" {
		if err := c.SetFocus(conv, focused); err != nil {
			t.Fatal(err)
		}
	}
	return ch
}

// notifyEvents collects the notify events on ch until a status sentinel.
func drainNotifies(ch <-chan ipc.Event, wait time.Duration) []ipc.NotifyPayload {
	var out []ipc.NotifyPayload
	timeout := time.After(wait)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if ev.Kind == ipc.KindNotify {
				out = append(out, *ev.Notify)
			}
		case <-timeout:
			return out
		}
	}
}

func TestAttachedUIGetsTheNotificationInsteadOfTheOS(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.Hook = "hook-cmd"
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "sd", false) // attached, not focused
	push(e, "d1", "dana", "m1", "ping", 0)
	ev := waitEvent(t, events, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	want := ipc.NotifyPayload{ConvID: "d1", ConvName: "dana", Sender: "Dana", Text: "ping", DM: true}
	if *ev.Notify != want {
		t.Fatalf("UI payload = %+v, want %+v", *ev.Notify, want)
	}
	// The hook runs whichever route is used; the OS is not asked.
	waitUntil(t, "hook", func() bool { return len(rec.hookCalls()) == 1 })
	if got := rec.hookCalls()[0]; got != want {
		t.Errorf("hook payload = %+v, want %+v", got, want)
	}
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("OS notified although a UI took it: %+v", got)
	}
}

func TestUIWithoutFocusReportCountsAsNotFocused(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "", false) // never called SetFocus
	push(e, "d1", "dana", "m1", "ping", 0)
	waitEvent(t, events, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("OS notified: %+v", got)
	}
}

func TestFocusedUIShowingTheConversationSuppressesEverything(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.Hook = "hook-cmd"
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "d1", true)
	push(e, "d1", "dana", "m1", "you are looking at me", 0)
	// A message in another conversation proves the first was decided.
	push(e, "sd", "sam", "s1", "elsewhere", 0)
	ev := waitEvent(t, events, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	if ev.Notify.ConvID != "sd" {
		t.Fatalf("UI got %+v, want only the other conversation", *ev.Notify)
	}
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("further UI notifications: %+v", got)
	}
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("OS notified: %+v", got)
	}
	if got := rec.hookCalls(); len(got) != 1 || got[0].ConvID != "sd" {
		t.Errorf("hook calls = %+v, want only the other conversation", got)
	}
}

func TestUIAttachedButUnfocusedStillGetsIt(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "d1", false) // showing d1, but its window is not focused
	push(e, "d1", "dana", "m1", "while you were away", 0)
	ev := waitEvent(t, events, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	if ev.Notify.Text != "while you were away" {
		t.Fatalf("payload = %+v", *ev.Notify)
	}
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("OS notified: %+v", got)
	}
}

func TestPlainSubscribeIsNotAUI(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	events := e.subscribe() // what `termchat wait` does
	push(e, "d1", "dana", "m1", "hi", 0)
	waitUntil(t, "OS notification", func() bool { return len(rec.osCalls()) == 1 })
	if got := rec.osCalls()[0]; got.Text != "hi" || got.ConvID != "d1" {
		t.Errorf("payload = %+v", got)
	}
	// A conversation event for the same message follows; no notify event
	// may be among what the plain subscriber receives.
	waitEvent(t, events, "message event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindMessage })
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("plain subscriber received notify events: %+v", got)
	}
}

func TestTerminalRouteOffFallsThroughToOS(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.Terminal = false
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "sd", true)
	push(e, "d1", "dana", "m1", "hi", 0)
	waitUntil(t, "OS notification", func() bool { return len(rec.osCalls()) == 1 })
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("UI got %+v with notify.terminal off", got)
	}
}

func TestOSRouteOffAndNoUIRaisesNothingButTheHook(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.OS = config.OSOff
	e.cfg.Notify.Hook = "hook-cmd"
	started(t, e, dm("d1", "dana"))
	push(e, "d1", "dana", "m1", "hi", 0)
	waitUntil(t, "hook", func() bool { return len(rec.hookCalls()) == 1 })
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("OS notified with notify.os off: %+v", got)
	}
}

type closingNotifier struct {
	noteRec
	closed chan struct{}
}

func (c *closingNotifier) Close() { close(c.closed) }

func TestNewNotifierIsUsedAndClosedOnShutdown(t *testing.T) {
	e := newEnv(t)
	n := &closingNotifier{closed: make(chan struct{})}
	var gotCfg bool
	e.adjust = func(o *Options) {
		o.NewNotifier = func(cfg *config.Config, log *slog.Logger) Notifier {
			gotCfg = cfg == e.cfg && log != nil
			return n
		}
	}
	e.start()
	e.stop()
	select {
	case <-n.closed:
	default:
		t.Fatal("notifier not closed when Run returned")
	}
	if !gotCfg {
		t.Error("factory did not receive the config and logger")
	}
}

func TestUIThatCannotShowNotificationsFallsThroughToOS(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	events := uiClientCan(t, false, "sd", false)
	push(e, "d1", "dana", "m1", "ping", 0)
	waitUntil(t, "OS notification", func() bool { return len(rec.osCalls()) == 1 })
	if got := rec.osCalls()[0]; got.Text != "ping" {
		t.Errorf("OS payload = %+v", got)
	}
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("incapable UI received %+v", got)
	}
}

func TestNotifyEventReachesOnlyTheCapableUI(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	incapable := uiClientCan(t, false, "sd", false)
	capable := uiClientCan(t, true, "sd", false)
	push(e, "d1", "dana", "m1", "ping", 0)
	ev := waitEvent(t, capable, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	if ev.Notify.Text != "ping" {
		t.Fatalf("payload = %+v", *ev.Notify)
	}
	if got := drainNotifies(incapable, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("incapable UI received %+v", got)
	}
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("OS notified although a capable UI took it: %+v", got)
	}
}

func TestFocusedIncapableUIStillSuppresses(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	uiClientCan(t, false, "d1", true)
	push(e, "d1", "dana", "m1", "you are looking at me", 0)
	sentinel(t, e, rec)
	if got := dropSentinels(rec.osCalls()); len(got) != 0 {
		t.Errorf("notified %+v although a focused UI shows the conversation", got)
	}
}

func TestOSAlwaysPostsAlongsideAttachedUI(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.OS = config.OSAlways
	e.cfg.Notify.Hook = "hook-cmd"
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "sd", false)
	push(e, "d1", "dana", "m1", "ping", 0)
	want := ipc.NotifyPayload{ConvID: "d1", ConvName: "dana", Sender: "Dana", Text: "ping", DM: true}
	ev := waitEvent(t, events, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	if *ev.Notify != want {
		t.Fatalf("UI payload = %+v, want %+v", *ev.Notify, want)
	}
	waitUntil(t, "OS notification", func() bool { return len(rec.osCalls()) == 1 })
	waitUntil(t, "hook", func() bool { return len(rec.hookCalls()) == 1 })
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("further UI notifications: %+v", got)
	}
	if got := rec.osCalls(); len(got) != 1 || got[0] != want {
		t.Errorf("OS calls = %+v, want exactly %+v", got, want)
	}
}

func TestOSAlwaysFocusedConversationRaisesNothing(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.OS = config.OSAlways
	e.cfg.Notify.Hook = "hook-cmd"
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "d1", true)
	push(e, "d1", "dana", "m1", "you are looking at me", 0)
	// A message in another conversation proves the first was decided.
	push(e, "sd", "sam", "s1", "elsewhere", 0)
	waitUntil(t, "OS notification", func() bool { return len(rec.osCalls()) == 1 })
	ev := waitEvent(t, events, "notify event", func(ev ipc.Event) bool { return ev.Kind == ipc.KindNotify })
	if ev.Notify.ConvID != "sd" {
		t.Fatalf("UI got %+v, want only the other conversation", *ev.Notify)
	}
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("further UI notifications: %+v", got)
	}
	if got := rec.osCalls(); len(got) != 1 || got[0].ConvID != "sd" {
		t.Errorf("OS calls = %+v, want only the other conversation", got)
	}
	if got := rec.hookCalls(); len(got) != 1 || got[0].ConvID != "sd" {
		t.Errorf("hook calls = %+v, want only the other conversation", got)
	}
}

func TestOSAlwaysWithTerminalOffPostsOnce(t *testing.T) {
	e, rec := noteEnv(t)
	e.cfg.Notify.OS = config.OSAlways
	e.cfg.Notify.Terminal = false
	started(t, e, dm("d1", "dana"))
	events := uiClient(t, "sd", false)
	push(e, "d1", "dana", "m1", "hi", 0)
	waitUntil(t, "OS notification", func() bool { return len(rec.osCalls()) == 1 })
	if got := drainNotifies(events, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("UI got %+v with notify.terminal off", got)
	}
	if got := rec.osCalls(); len(got) != 1 {
		t.Errorf("OS calls = %+v, want exactly one", got)
	}
}
