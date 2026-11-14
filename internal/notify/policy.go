// Package notify decides which messages deserve the user's attention and
// carries them out: the escape sequences a terminal turns into a
// notification, the operating system's notification service, and a
// user-configured hook command. Message text only ever travels as data, as an
// argument or environment variable, never inside a script or command string.
package notify

import (
	"slices"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// Notification is what a notification shows.
type Notification = ipc.NotifyPayload

// Room modes, as configured per conversation; any other value means
// mentions only.
const (
	modeAll   = "all"
	modeMuted = "muted"
)

// Context is what ShouldNotify needs besides the message itself.
type Context struct {
	// Me is the user's handle.
	Me string
	// RoomMode is the conversation's configured mode; empty means mentions.
	// It does not apply to direct messages.
	RoomMode string
	// Unread is true while the message is after the conversation's read
	// position, which another device may have moved.
	Unread bool
	// Focused is true while an attached UI shows the message's conversation
	// in a focused window.
	Focused bool
}

// ShouldNotify reports whether m, a message of conv, needs the user's
// attention.
func ShouldNotify(m store.Message, conv model.Conversation, c Context) bool {
	switch {
	case m.Deleted, !c.Unread, c.Focused, m.Author == c.Me:
		return false
	case conv.Kind == model.ConvDM:
		return true
	}
	switch c.RoomMode {
	case modeMuted:
		return false
	case modeAll:
		return true
	default:
		return slices.Contains(m.Mentions, c.Me)
	}
}
