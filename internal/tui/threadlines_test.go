package tui

import (
	"reflect"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
)

// sameAsUncached checks the thread's kept lines against lines laid out
// from nothing, at the pane's width and layout as it stands.
func sameAsUncached(t *testing.T, h *tester, after string) {
	t.Helper()
	h.view()
	g := h.m.geom()
	w, _ := h.m.threadPane(g)
	got, want := h.m.threadLines(w, g.mode), h.m.layLines(w, g.mode, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after %s the kept lines differ from a fresh layout:\n got %v\nwant %v", after, got, want)
	}
}

func TestKeptThreadLinesFollowEveryChange(t *testing.T) {
	f := fixture()
	h := opened(t, f, 120, 20)
	sameAsUncached(t, h, "opening")
	reread := func(m ipc.Message) {
		h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m}})
	}

	f.stored["r-platform"][1].Text, f.stored["r-platform"][1].Edited = "yes, 20 min ago", true
	reread(f.stored["r-platform"][1])
	mustContain(t, h.view(), "yes, 20 min ago (edited)")
	sameAsUncached(t, h, "an edit")

	f.stored["r-platform"][5].Deleted = true
	reread(f.stored["r-platform"][5])
	mustContain(t, h.view(), "message deleted")
	sameAsUncached(t, h, "a delete")

	f.outbox["r-platform"][0].Status = ipc.MsgFailed
	f.reasons = map[string][]ipc.OutboxItem{"r-platform": {{MsgID: "o1", ConvID: "r-platform",
		State: "failed", Err: "expired"}}}
	h.send(eventMsg{ipc.Event{Kind: ipc.KindOutbox, Outbox: &ipc.OutboxItem{MsgID: "o1", ConvID: "r-platform"}}})
	mustContain(t, h.view(), "failed (expired) — retry?")
	sameAsUncached(t, h, "a status change")

	h.key("up")
	mustContain(t, h.view(), "failed (expired) — r retry, x discard")
	sameAsUncached(t, h, "selecting a failed message")
	h.key("up")
	sameAsUncached(t, h, "moving the selection")

	for _, w := range []int{90, 60, 30} {
		h.size(w, 20)
		sameAsUncached(t, h, "a width change")
	}
}
