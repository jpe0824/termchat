// Package connector defines the network-neutral client interface. Everything
// above a connector (the background process, CLI and TUI) talks only to this
// interface; each chat network supplies one implementation.
package connector

import (
	"context"
	"errors"

	"github.com/jpe0824/termchat/internal/model"
)

// ConnState is the connection state of a connector.
type ConnState string

const (
	StateConnecting ConnState = "connecting"
	StateConnected  ConnState = "connected"
	StateOffline    ConnState = "offline"
	// The three states below are terminal: the connector stops dialling and
	// only a new connector (after the user acts) leaves them.
	StateNotAuthorised   ConnState = "not_authorised"
	StateHostKeyChanged  ConnState = "host_key_changed"
	StateVersionMismatch ConnState = "version_mismatch"
)

// Terminal reports whether the connector never leaves s on its own.
func (s ConnState) Terminal() bool {
	switch s {
	case StateNotAuthorised, StateHostKeyChanged, StateVersionMismatch:
		return true
	}
	return false
}

// Update is one thing the connector learned without being asked. At most one
// field is set; none set means the state changed and the caller should read
// State.
type Update struct {
	Event *model.Event
	Read  *ReadUpdate
	Conv  *model.Conversation
}

// ReadUpdate reports that the caller's read marker moved on another device.
type ReadUpdate struct {
	ConvID string
	Seq    int64
}

// Caps lists what the network supports.
type Caps struct{ Edit, Delete, Rooms, Agents bool }

// Room operation kinds for RoomOp.Kind.
const (
	RoomCreate = "create"
	RoomJoin   = "join"
	RoomLeave  = "leave"
	RoomInvite = "invite"
	RoomRemove = "remove"
	RoomRename = "rename"
)

// RoomOp is one room operation. Which fields apply depends on Kind: create
// uses Name and Open; join and leave use Conv; invite and remove use Conv and
// Handle; rename uses Conv and Name.
type RoomOp struct {
	Kind   string
	Conv   string
	Name   string
	Handle string
	Open   bool
}

// Errors returned by every Connector method. Implementations wrap them with
// the server's message, so test with errors.Is and show err.Error() to users.
var (
	ErrOffline         = errors.New("offline")
	ErrNotAuthorised   = errors.New("not authorised")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
	ErrInvalid         = errors.New("invalid request")
	ErrTooLarge        = errors.New("too large")
	ErrRateLimited     = errors.New("rate limited")
	ErrVersionMismatch = errors.New("version mismatch")
	ErrInternal        = errors.New("internal error")
)

// Connector is one account's connection to a chat network.
//
// Updates delivers pushes in the order received and never drops one: when the
// consumer falls behind, the connector stops reading from the network. The
// consumer should therefore drain Updates from its own goroutine and must not
// wait on a request method from the goroutine that drains it. Methods are
// safe for concurrent use and return ErrOffline while not connected.
type Connector interface {
	// Run owns the connection until ctx ends, reconnecting with backoff, and
	// returns nil once ctx is cancelled.
	Run(ctx context.Context) error
	State() ConnState
	Updates() <-chan Update
	Me() model.Person
	Caps() Caps

	People(ctx context.Context) ([]model.Person, error)
	Conversations(ctx context.Context) (mine, openRooms []model.Conversation, err error)
	// History returns up to limit events older than beforeSeq (0 means the
	// newest), oldest first.
	History(ctx context.Context, conv string, beforeSeq int64, limit int) ([]model.Event, error)
	// Sync returns events after each cursor. When more is true the caller
	// repeats the call with advanced cursors. gone lists conversations the
	// caller is no longer in.
	Sync(ctx context.Context, cursors map[string]int64) (events []model.Event, more bool, gone []string, err error)
	// Send posts a message. Resending a msgID returns the original event
	// rather than an error.
	Send(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error)
	Edit(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error)
	Delete(ctx context.Context, conv, msgID string) (model.Event, error)
	MarkRead(ctx context.Context, conv string, seq int64) error
	OpenDM(ctx context.Context, handle string) (model.Conversation, error)
	Room(ctx context.Context, op RoomOp) (model.Conversation, error)
	DeviceInvite(ctx context.Context) (code string, err error)
	CreateAgent(ctx context.Context, handle, displayName string) (code string, err error)
	AdminInvite(ctx context.Context, handle, displayName string) (code string, err error)
}
