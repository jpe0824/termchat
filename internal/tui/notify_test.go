package tui

import (
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
)

func TestNotificationBell(t *testing.T) {
	p := &ipc.NotifyPayload{Sender: "dana", Text: "hi", DM: true}
	tests := []struct {
		osc  string
		want string
	}{
		{"9", "\x1b]9;dana: hi\x07\a"},
		// Herdr plays its own sound for a status report; a bell would add a second.
		{"7501", "\x1b]7501;app=termchat:state=working\x07\x1b]7501;app=termchat:state=done\x07"},
		{"", ""},
	}
	for _, tt := range tests {
		m := &Model{osc: tt.osc}
		if got := m.notification(p); got != tt.want {
			t.Errorf("osc %q: notification = %q, want %q", tt.osc, got, tt.want)
		}
	}
}
