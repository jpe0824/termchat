// Package proto defines the client-server wire protocol: newline-delimited
// JSON frames carrying typed payloads.
package proto

import (
	"encoding/json"

	"github.com/jpe0824/termchat/internal/model"
)

const (
	// Version is the protocol version exchanged in hello.
	Version = 1
	// MaxFrame is the largest encoded frame, excluding the newline.
	MaxFrame = 64 << 10
)

// Frame is one protocol message. Requests carry a non-zero ID that the
// response echoes; server pushes use ID 0.
type Frame struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a failed response.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Error codes.
const (
	CodeNotAuthorised   = "not_authorised"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeInvalid         = "invalid"
	CodeTooLarge        = "too_large"
	CodeRateLimited     = "rate_limited"
	CodeVersionMismatch = "version_mismatch"
)

// Request and push frame types.
const (
	TypeHello             = "hello"
	TypeInviteRedeem      = "invite.redeem"
	TypePeopleList        = "people.list"
	TypeConvList          = "conv.list"
	TypeConvHistory       = "conv.history"
	TypeConvSync          = "conv.sync"
	TypeMsgSend           = "msg.send"
	TypeMsgEdit           = "msg.edit"
	TypeMsgDelete         = "msg.delete"
	TypeReadSet           = "read.set"
	TypeDMOpen            = "dm.open"
	TypeRoomCreate        = "room.create"
	TypeRoomJoin          = "room.join"
	TypeRoomLeave         = "room.leave"
	TypeRoomInvite        = "room.invite"
	TypeRoomRemove        = "room.remove"
	TypeRoomRename        = "room.rename"
	TypeDeviceInvite      = "device.invite"
	TypeAgentCreate       = "agent.create"
	TypeAdminInvite       = "admin.invite"
	TypeAdminRemovePerson = "admin.remove_person"
	TypeAdminRemoveKey    = "admin.remove_key"

	// Server pushes.
	TypeEvent       = "event"
	TypeRead        = "read"
	TypeConvUpdated = "conv.updated"
)

// HelloReq opens a session. HelloRes.Person is nil when the key is unknown.
type HelloReq struct {
	Version int `json:"version"`
}

type HelloRes struct {
	Version int           `json:"version"`
	Person  *model.Person `json:"person,omitempty"`
}

type InviteRedeemReq struct {
	Code string `json:"code"`
}

type InviteRedeemRes struct {
	Person model.Person `json:"person"`
}

type PeopleListReq struct{}

type PeopleListRes struct {
	People []model.Person `json:"people"`
}

type ConvListReq struct{}

type ConvListRes struct {
	Mine      []model.Conversation `json:"mine"`
	OpenRooms []model.Conversation `json:"open_rooms"`
}

type ConvHistoryReq struct {
	Conv      string `json:"conv"`
	BeforeSeq int64  `json:"before_seq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ConvHistoryRes struct {
	Events []model.Event `json:"events"`
}

// ConvSyncReq maps conversation ID to the last Seq the client holds.
type ConvSyncReq struct {
	Cursors map[string]int64 `json:"cursors"`
}

type ConvSyncRes struct {
	Events []model.Event `json:"events"`
}

type MsgSendReq struct {
	Conv     string     `json:"conv"`
	MsgID    string     `json:"msg_id"`
	Body     model.Body `json:"body"`
	Mentions []string   `json:"mentions,omitempty"`
}

// MsgSendRes reports Duplicate when MsgID was already accepted; Event is then
// the original event.
type MsgSendRes struct {
	Event     model.Event `json:"event"`
	Duplicate bool        `json:"duplicate,omitempty"`
}

type MsgEditReq struct {
	Conv     string     `json:"conv"`
	MsgID    string     `json:"msg_id"`
	Body     model.Body `json:"body"`
	Mentions []string   `json:"mentions,omitempty"`
}

type MsgDeleteReq struct {
	Conv  string `json:"conv"`
	MsgID string `json:"msg_id"`
}

type ReadSetReq struct {
	Conv string `json:"conv"`
	Seq  int64  `json:"seq"`
}

type DMOpenReq struct {
	Handle string `json:"handle"`
}

type DMOpenRes struct {
	Conv model.Conversation `json:"conv"`
}

type RoomCreateReq struct {
	Name string `json:"name"`
	Open bool   `json:"open,omitempty"`
}

type RoomCreateRes struct {
	Conv model.Conversation `json:"conv"`
}

type RoomJoinReq struct {
	Conv string `json:"conv"`
}

type RoomJoinRes struct {
	Conv model.Conversation `json:"conv"`
}

type RoomLeaveReq struct {
	Conv string `json:"conv"`
}

type RoomInviteReq struct {
	Conv   string `json:"conv"`
	Handle string `json:"handle"`
}

type RoomInviteRes struct {
	Conv model.Conversation `json:"conv"`
}

type RoomRemoveReq struct {
	Conv   string `json:"conv"`
	Handle string `json:"handle"`
}

type RoomRemoveRes struct {
	Conv model.Conversation `json:"conv"`
}

type RoomRenameReq struct {
	Conv string `json:"conv"`
	Name string `json:"name"`
}

type RoomRenameRes struct {
	Conv model.Conversation `json:"conv"`
}

type DeviceInviteReq struct{}

type DeviceInviteRes struct {
	Code string `json:"code"`
}

type AgentCreateReq struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
}

type AgentCreateRes struct {
	Code string `json:"code"`
}

type AdminInviteReq struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
}

type AdminInviteRes struct {
	Code string `json:"code"`
}

type AdminRemovePersonReq struct {
	Handle string `json:"handle"`
}

type AdminRemoveKeyReq struct {
	Fingerprint string `json:"fingerprint"`
}

// Server pushes (Frame.ID 0).

// EventPush delivers one new event.
type EventPush struct {
	Event model.Event `json:"event"`
}

// ReadPush tells the caller's other devices that a read marker moved.
type ReadPush struct {
	Conv string `json:"conv"`
	Seq  int64  `json:"seq"`
}

// ConvUpdatedPush delivers a changed conversation.
type ConvUpdatedPush struct {
	Conv model.Conversation `json:"conv"`
}
