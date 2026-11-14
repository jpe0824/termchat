// Package ipc is the protocol between the background process and its clients
// (TUI and CLI) over a Unix socket: newline-delimited JSON requests and
// responses matched by id, plus a stream of events on a subscribed
// connection. The first request on a connection must be the version
// handshake.
package ipc

import (
	"encoding/json"
	"path/filepath"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/paths"
	"github.com/jpe0824/termchat/internal/store"
)

// Version is the protocol version. Both ends must speak the same one.
const Version = 1

// Socket and lock file names inside paths.Runtime().
const (
	SocketName = "daemon.sock"
	LockName   = "daemon.lock"
)

// SocketPath is where the background process listens.
func SocketPath() (string, error) {
	dir, err := paths.Runtime()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SocketName), nil
}

// Status describes the background process. Account is empty when no account
// is configured.
type Status struct {
	Account string
	Me      model.Person
	Conn    connector.ConnState
	// Pending counts outbox items still to be sent.
	Pending int
	// Syncing is true while the catch-up after connecting or the history
	// backfill is running.
	Syncing bool
}

// Chats is the conversation list: the user's own conversations and the open
// rooms they have not joined.
type Chats struct {
	Mine []store.ConvView
	Open []model.Conversation
}

// Event kinds.
const (
	// KindMessage: a message was added or changed; Message is set.
	KindMessage = "message"
	// KindConv: a conversation's view changed; Conv is set, and Removed is
	// true when the user is no longer in it.
	KindConv = "conv"
	// KindOutbox: an outbox item changed state; Message shows it with its
	// Status and Outbox holds the item. Message.Deleted marks a discarded item.
	KindOutbox = "outbox"
	// KindStatus: Status is set.
	KindStatus = "status"
	// KindNotify: Notify is set.
	KindNotify = "notify"
)

// Event is one pushed change on a subscribed connection.
type Event struct {
	Kind    string
	Message *store.Message    `json:",omitempty"`
	Conv    *store.ConvView   `json:",omitempty"`
	Removed bool              `json:",omitempty"`
	Outbox  *store.OutboxItem `json:",omitempty"`
	Status  *Status           `json:",omitempty"`
	Notify  *NotifyPayload    `json:",omitempty"`
}

// NotifyPayload is a notification the background process decided to raise.
type NotifyPayload struct {
	ConvID, ConvName, Sender, Text string
	Mention, DM                    bool
}

// Wire format. A request carries the operation's arguments in one flat
// struct; unused fields are omitted.
type request struct {
	ID   uint64 `json:"id"`
	Op   string `json:"op"`
	Args *args  `json:"args,omitempty"`
}

type args struct {
	Version   int               `json:",omitempty"`
	Conv      string            `json:",omitempty"`
	MsgID     string            `json:",omitempty"`
	Text      string            `json:",omitempty"`
	Source    string            `json:",omitempty"`
	Query     string            `json:",omitempty"`
	Ref       string            `json:",omitempty"`
	Handle    string            `json:",omitempty"`
	Name      string            `json:",omitempty"`
	Server    string            `json:",omitempty"`
	Code      string            `json:",omitempty"`
	BeforeSeq int64             `json:",omitempty"`
	Limit     int               `json:",omitempty"`
	Focused   bool              `json:",omitempty"`
	Room      *connector.RoomOp `json:",omitempty"`
}

// A frame from the server is either a response (ID set) or an event.
type frame struct {
	ID     uint64          `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
	Event  *Event          `json:"event,omitempty"`
}

// Operation names.
const (
	opHello        = "hello"
	opStatus       = "status"
	opChats        = "chats"
	opMessages     = "messages"
	opSend         = "send"
	opEdit         = "edit"
	opDelete       = "delete"
	opRetry        = "retry"
	opDiscard      = "discard"
	opMarkRead     = "mark_read"
	opSearch       = "search"
	opResolve      = "resolve"
	opOpenDM       = "open_dm"
	opRoom         = "room"
	opDeviceInvite = "device_invite"
	opCreateAgent  = "create_agent"
	opAdminInvite  = "admin_invite"
	opJoin         = "join"
	opSubscribe    = "subscribe"
	opUnsubscribe  = "unsubscribe"
	opSetFocus     = "set_focus"
	opStop         = "stop"
)
