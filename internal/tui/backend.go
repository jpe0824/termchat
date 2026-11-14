// Package tui is the terminal chat interface. It talks to the background
// process only through Backend and never touches the store or the network.
package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// Backend is the part of *ipc.Client the interface uses.
type Backend interface {
	Status() (ipc.Status, error)
	Chats() ([]ipc.ConvView, []model.Conversation, error)
	Messages(conv string, beforeSeq int64, limit int) ([]ipc.Message, error)
	SubscribeUI(ctx context.Context, canNotify bool) (<-chan ipc.Event, error)
	SubscriptionErr() error
	Close() error

	// Send queues text and returns once it is queued, even offline.
	Send(conv, text, source string) (string, error)
	// Edit and Delete need the server: offline they fail.
	Edit(conv, msgID, text string) error
	Delete(conv, msgID string) error
	// Retry and Discard act on a failed unsent item.
	Retry(msgID string) error
	Discard(msgID string) error
	// Outbox lists conv's unsent items with the reason each one failed.
	Outbox(conv string) ([]ipc.OutboxItem, error)
	MarkRead(conv string) error
	// SetFocus tells the background process which conversation this
	// connection shows ("" for none) and whether the terminal has focus.
	SetFocus(conv string, focused bool) error
}

// Options are the settings and outside hooks the interface needs. Zero
// values fall back to the real clock, environment and local time zone.
type Options struct {
	// Splash shows the opening screen (config "splash").
	Splash bool
	// MarkRead marks the conversation being looked at read (config
	// "mark_read"); without it the interface never marks anything read.
	MarkRead bool
	// Version is shown on the opening screen.
	Version string
	// Server returns the configured server address, "host:port"; it may read
	// a file, so it runs off the update loop.
	Server func() string
	// Env looks up an environment variable; NO_COLOR turns colour off.
	Env func(string) string
	// Location is the time zone for message times.
	Location *time.Location
	// Tick schedules msg after d for the update loop.
	Tick func(d time.Duration, msg tea.Msg) tea.Cmd
	// After is the clock the reconnect loop waits on.
	After func(d time.Duration) <-chan time.Time
	// Dial connects to a running background process; it never starts one.
	Dial func(ctx context.Context) (Backend, error)
	// Start connects and starts the background process when none runs.
	Start func(ctx context.Context) (Backend, error)
	// Send delivers a message to the running program from the subscription
	// goroutine. Without it no subscription is kept.
	Send func(tea.Msg)
}

func (o *Options) fill() {
	if o.Env == nil {
		o.Env = func(string) string { return "" }
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Tick == nil {
		o.Tick = func(d time.Duration, msg tea.Msg) tea.Cmd {
			return tea.Tick(d, func(time.Time) tea.Msg { return msg })
		}
	}
	if o.After == nil {
		o.After = time.After
	}
}
