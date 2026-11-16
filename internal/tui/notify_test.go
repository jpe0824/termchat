package tui

import (
	"slices"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/notify"
)

func TestNotificationBell(t *testing.T) {
	p := &ipc.NotifyPayload{Sender: "dana", Text: "hi", DM: true}
	tests := []struct {
		osc  string
		want string
	}{
		{"9", "\x1b]9;dana: hi\x07\a"},
		// Herdr plays its own sound for a status report; a bell would add a second.
		{"7501", "\x1b]7501;app=termchat:state=working\x07"},
		{"", ""},
	}
	for _, tt := range tests {
		m := &Model{osc: tt.osc}
		if got := m.notification(p); got != tt.want {
			t.Errorf("osc %q: notification = %q, want %q", tt.osc, got, tt.want)
		}
	}
}

// Herdr alerts on a change of status, and a pane still done from an earlier
// message would show none if working and done arrived in one write. Done
// follows after herdrDoneAfter instead.
func TestHerdrDoneFollowsWorking(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, func(o *Options) { o.NotifyOSC = "7501" })
	h.ticks = nil
	p := ipc.NotifyPayload{ConvID: "dm-dana", ConvName: "dana", Sender: "Dana", Text: "hi", DM: true}
	h.send(eventsMsg{evs: []ipc.Event{{Kind: ipc.KindNotify, Notify: &p}}})
	if want := []string{"\x1b]7501;app=termchat:state=working\x07"}; !slices.Equal(h.raws, want) {
		t.Fatalf("wrote %q, want %q", h.raws, want)
	}
	var found bool
	for _, tk := range h.ticks {
		if _, ok := tk.msg.(herdrDoneMsg); ok {
			found = true
			if tk.d != herdrDoneAfter {
				t.Errorf("done scheduled after %v, want %v", tk.d, herdrDoneAfter)
			}
		}
	}
	if !found {
		t.Fatalf("no done scheduled; ticks %v", h.ticks)
	}
	h.raws = nil
	h.fireTicks()
	if want := []string{notify.HerdrDone}; !slices.Equal(h.raws, want) {
		t.Errorf("after the delay wrote %q, want %q", h.raws, want)
	}
}
