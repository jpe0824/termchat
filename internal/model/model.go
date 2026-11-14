// Package model holds the network-neutral chat data types.
package model

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// EventKind names what an Event records.
type EventKind string

const (
	MessagePosted  EventKind = "message.posted"
	MessageEdited  EventKind = "message.edited"
	MessageDeleted EventKind = "message.deleted"
	MemberJoined   EventKind = "member.joined"
	MemberLeft     EventKind = "member.left"
	RoomRenamed    EventKind = "room.renamed"
)

// Body is an opaque message payload. The server stores and relays Data
// without interpreting it; Format tells clients how to render it.
type Body struct {
	Format string `json:"format"`
	Data   []byte `json:"data"`
}

// Event is one entry in a conversation's ordered log. Seq is assigned by the
// server and is dense per conversation.
type Event struct {
	ConvID string    `json:"conv_id"`
	Seq    int64     `json:"seq"`
	Kind   EventKind `json:"kind"`
	Actor  string    `json:"actor"`
	At     time.Time `json:"at"`
	MsgID  string    `json:"msg_id,omitempty"`
	Body   *Body     `json:"body,omitempty"`
	// Mentions are handles chosen by the sender; they travel beside the body
	// because the server never parses it.
	Mentions []string `json:"mentions,omitempty"`
	// Target is the affected handle for member events.
	Target string `json:"target,omitempty"`
	// Name is the new room name for RoomRenamed.
	Name string `json:"name,omitempty"`
}

// ConvKind distinguishes direct messages from rooms.
type ConvKind string

const (
	ConvDM   ConvKind = "dm"
	ConvRoom ConvKind = "room"
)

// Conversation describes a DM or room from one caller's point of view:
// ReadSeq is the caller's read marker, LastSeq the newest event.
type Conversation struct {
	ID      string   `json:"id"`
	Kind    ConvKind `json:"kind"`
	Name    string   `json:"name,omitempty"`
	Open    bool     `json:"open,omitempty"`
	Creator string   `json:"creator,omitempty"`
	Members []string `json:"members,omitempty"`
	LastSeq int64    `json:"last_seq"`
	ReadSeq int64    `json:"read_seq"`
}

// Peer is the other person in a DM: the first member who is not me, or me
// when there is none.
func (c Conversation) Peer(me string) string {
	for _, h := range c.Members {
		if h != me {
			return h
		}
	}
	return me
}

// Person is a chat identity. Owner is the human handle that owns an agent.
type Person struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
	Agent       bool   `json:"agent,omitempty"`
	Admin       bool   `json:"admin,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// NameRule states what ValidName accepts, for messages to people.
const NameRule = "use lowercase letters, digits, - and _, starting with a letter or digit, at most 32 characters"

// ValidName reports whether s is a legal handle or room name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// MaxTextBytes is the longest message text a client sends, in bytes.
const MaxTextBytes = 8192

// Reasons CheckText refuses a message text.
var (
	ErrEmptyText   = errors.New("message text is empty")
	ErrTextTooLong = errors.New("message text is too long")
)

// CheckText reports whether text may be sent: ErrEmptyText when nothing but
// space is left after trimming, ErrTextTooLong when it is over MaxTextBytes.
// Callers word the refusal for their own audience.
func CheckText(text string) error {
	switch {
	case strings.TrimSpace(text) == "":
		return ErrEmptyText
	case len(text) > MaxTextBytes:
		return ErrTextTooLong
	}
	return nil
}

// NormalizeName trims surrounding space, strips one leading @ or #, and
// lowercases. The result is not guaranteed valid; check it with ValidName.
func NormalizeName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 0 && (s[0] == '@' || s[0] == '#') {
		s = s[1:]
	}
	return strings.ToLower(s)
}
