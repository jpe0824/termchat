package daemon

import (
	"cmp"
	"slices"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	// notifyTextMax is the most runes of message text a notification carries.
	notifyTextMax = 200
	// notifyNameMax is the most runes of a sender or conversation name.
	notifyNameMax = 64
)

// payload describes m for a notification.
func payload(m store.Message, conv model.Conversation, me string) ipc.NotifyPayload {
	p := ipc.NotifyPayload{
		ConvID: conv.ID, ConvName: conv.Name, DM: conv.Kind == model.ConvDM,
		Sender:  shorten(sanitize.Line(cmp.Or(m.AuthorName, m.Author)), notifyNameMax),
		Text:    shorten(sanitize.Line(m.Text), notifyTextMax),
		Mention: slices.Contains(m.Mentions, me),
	}
	if p.DM {
		p.ConvName = conv.Peer(me)
	}
	p.ConvName = shorten(sanitize.Line(p.ConvName), notifyNameMax)
	return p
}

// shorten cuts s to max runes, marking the cut with an ellipsis.
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
