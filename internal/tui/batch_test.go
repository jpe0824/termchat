package tui

import (
	"slices"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/notify"
)

func TestDrainTakesWhatIsWaitingWithoutWaiting(t *testing.T) {
	ch := make(chan ipc.Event, 4)
	ch <- ipc.Event{Kind: ipc.KindConv}
	ch <- ipc.Event{Kind: ipc.KindOutbox}
	evs, open := drain(ch, ipc.Event{Kind: ipc.KindMessage})
	if kinds := []string{evs[0].Kind, evs[1].Kind, evs[2].Kind}; len(evs) != 3 || !open ||
		!slices.Equal(kinds, []string{ipc.KindMessage, ipc.KindConv, ipc.KindOutbox}) {
		t.Errorf("drained %v open %v", evs, open)
	}
	ch <- ipc.Event{Kind: ipc.KindConv}
	close(ch)
	if evs, open := drain(ch, ipc.Event{Kind: ipc.KindMessage}); len(evs) != 2 || open {
		t.Errorf("drained %v open %v, want two events and the channel closed", evs, open)
	}
}

// One batch holds at most maxBatch events; the rest wait on the channel for
// the next.
func TestDrainStopsAtTheBatchBound(t *testing.T) {
	ch := make(chan ipc.Event, 300)
	for range 300 {
		ch <- ipc.Event{Kind: ipc.KindConv}
	}
	evs, open := drain(ch, ipc.Event{Kind: ipc.KindMessage})
	if len(evs) != 256 || !open {
		t.Fatalf("drained %d open %v, want 256 and open", len(evs), open)
	}
	if left := len(ch); left != 300-255 {
		t.Fatalf("%d left on the channel, want %d", left, 300-255)
	}
}

// A batch of events re-reads what they touch once, and writes their
// notifications in the order they came.
func TestEventBatchRereadsOnceAndKeepsNotificationOrder(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, func(o *Options) { o.NotifyOSC = "99" })
	a := ipc.NotifyPayload{ConvID: "dm-dana", ConvName: "dana", Sender: "Dana", Text: "first", DM: true}
	b := ipc.NotifyPayload{ConvID: "dm-sam", ConvName: "sam", Sender: "Sam", Text: "second", DM: true}
	chats, thread := f.callCount("chats"), f.callCount("messages r-platform")
	m := ipc.Message{ConvID: "r-platform"}
	h.send(eventsMsg{evs: []ipc.Event{
		{Kind: ipc.KindNotify, Notify: &a},
		{Kind: ipc.KindMessage, Message: &m},
		{Kind: ipc.KindConv, Conv: &f.convs[0]},
		{Kind: ipc.KindMessage, Message: &m},
		{Kind: ipc.KindNotify, Notify: &b},
	}})
	if want := []string{notify.OSC("99", a) + "\a" + notify.OSC("99", b) + "\a"}; !slices.Equal(h.raws, want) {
		t.Errorf("wrote %q, want %q", h.raws, want)
	}
	if f.callCount("chats") != chats+1 || f.callCount("messages r-platform") != thread+1 {
		t.Errorf("calls %v, want one chat list read and one thread read", f.calls)
	}
}

// A re-read for a new message reaches back only to it; one that comes back
// short of what is held is replaced by a read of the whole window.
func TestMessageEventSizesTheReread(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", numbered("r-platform", 1, 40)...)
	h := opened(t, f, 90, 20)
	f.addStored("r-platform", numbered("r-platform", 41, 41)...)
	m := f.stored["r-platform"][40]
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m}})
	if f.callCount("messages r-platform 0 2") != 1 || len(h.m.thread.stored) != 41 {
		t.Errorf("calls %v, held %d, want one read of 2 and 41 held", f.calls, len(h.m.thread.stored))
	}
	// 42 and 43 arrived, but the event names 42: the sized read misses 42.
	f.addStored("r-platform", numbered("r-platform", 42, 44)...)
	m = f.stored["r-platform"][42]
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m}})
	if f.callCount("messages r-platform 0 3") != 1 || f.callCount("messages r-platform 0 50") != 2 {
		t.Errorf("calls %v, want a read of 3 then the whole window", f.calls)
	}
	if got := len(h.m.thread.stored); got != 44 {
		t.Errorf("held %d messages, want 44", got)
	}
	mustContain(t, h.view(), "line 44.")
}
