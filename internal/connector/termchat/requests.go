package termchat

import (
	"context"
	"errors"
	"fmt"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// call sends one request on the current connection and returns the decoded
// response. It returns ErrOffline at once when not connected.
func call[Res any](ctx context.Context, c *Client, typ string, req any) (Res, error) {
	return do[Res](ctx, c, typ, req, false)
}

// change is call for a request that alters server state. An error after the
// frame was written means the outcome is unknown, and carries
// connector.ErrUnknownOutcome.
func change[Res any](ctx context.Context, c *Client, typ string, req any) (Res, error) {
	return do[Res](ctx, c, typ, req, true)
}

func do[Res any](ctx context.Context, c *Client, typ string, req any, changes bool) (Res, error) {
	var res Res
	cn := c.current()
	if cn == nil {
		return res, connector.ErrOffline
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	err := cn.request(rctx, typ, req, &res)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		err = fmt.Errorf("%s timed out after %s: %w", typ, c.timeout, err)
	}
	var sent *inFlight
	if changes && errors.As(err, &sent) {
		err = fmt.Errorf("%w: %w", connector.ErrUnknownOutcome, err)
	}
	return res, err
}

func (c *Client) People(ctx context.Context) ([]model.Person, error) {
	res, err := call[proto.PeopleListRes](ctx, c, proto.TypePeopleList, proto.PeopleListReq{})
	return res.People, err
}

func (c *Client) Conversations(ctx context.Context) (mine, openRooms []model.Conversation, err error) {
	res, err := call[proto.ConvListRes](ctx, c, proto.TypeConvList, proto.ConvListReq{})
	return res.Mine, res.OpenRooms, err
}

func (c *Client) History(ctx context.Context, conv string, beforeSeq int64, limit int) ([]model.Event, error) {
	res, err := call[proto.ConvHistoryRes](ctx, c, proto.TypeConvHistory,
		proto.ConvHistoryReq{Conv: conv, BeforeSeq: beforeSeq, Limit: limit})
	return res.Events, err
}

func (c *Client) Sync(ctx context.Context, cursors map[string]int64) (events []model.Event, more bool, gone []string, err error) {
	res, err := call[proto.ConvSyncRes](ctx, c, proto.TypeConvSync, proto.ConvSyncReq{Cursors: cursors})
	return res.Events, res.More, res.Gone, err
}

func (c *Client) Send(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	res, err := change[proto.MsgSendRes](ctx, c, proto.TypeMsgSend,
		proto.MsgSendReq{Conv: conv, MsgID: msgID, Body: body, Mentions: mentions})
	return res.Event, err
}

func (c *Client) Edit(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	res, err := change[proto.MsgEditRes](ctx, c, proto.TypeMsgEdit,
		proto.MsgEditReq{Conv: conv, MsgID: msgID, Body: body, Mentions: mentions})
	return res.Event, err
}

func (c *Client) Delete(ctx context.Context, conv, msgID string) (model.Event, error) {
	res, err := change[proto.MsgDeleteRes](ctx, c, proto.TypeMsgDelete, proto.MsgDeleteReq{Conv: conv, MsgID: msgID})
	return res.Event, err
}

func (c *Client) MarkRead(ctx context.Context, conv string, seq int64) error {
	_, err := change[struct{}](ctx, c, proto.TypeReadSet, proto.ReadSetReq{Conv: conv, Seq: seq})
	return err
}

func (c *Client) OpenDM(ctx context.Context, handle string) (model.Conversation, error) {
	res, err := change[proto.DMOpenRes](ctx, c, proto.TypeDMOpen, proto.DMOpenReq{Handle: handle})
	return res.Conv, err
}

// Room runs one room operation. Leave answers with an empty payload, so its
// result carries only the conversation ID.
func (c *Client) Room(ctx context.Context, op connector.RoomOp) (model.Conversation, error) {
	switch op.Kind {
	case connector.RoomCreate:
		res, err := change[proto.RoomCreateRes](ctx, c, proto.TypeRoomCreate, proto.RoomCreateReq{Name: op.Name, Open: op.Open})
		return res.Conv, err
	case connector.RoomJoin:
		res, err := change[proto.RoomJoinRes](ctx, c, proto.TypeRoomJoin, proto.RoomJoinReq{Conv: op.Conv})
		return res.Conv, err
	case connector.RoomLeave:
		_, err := change[struct{}](ctx, c, proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: op.Conv})
		return model.Conversation{ID: op.Conv}, err
	case connector.RoomInvite:
		res, err := change[proto.RoomInviteRes](ctx, c, proto.TypeRoomInvite, proto.RoomInviteReq{Conv: op.Conv, Handle: op.Handle})
		return res.Conv, err
	case connector.RoomRemove:
		res, err := change[proto.RoomRemoveRes](ctx, c, proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: op.Conv, Handle: op.Handle})
		return res.Conv, err
	case connector.RoomRename:
		res, err := change[proto.RoomRenameRes](ctx, c, proto.TypeRoomRename, proto.RoomRenameReq{Conv: op.Conv, Name: op.Name})
		return res.Conv, err
	}
	return model.Conversation{}, fmt.Errorf("%w: unknown room operation %q", connector.ErrInvalid, op.Kind)
}

func (c *Client) DeviceInvite(ctx context.Context) (string, error) {
	res, err := change[proto.DeviceInviteRes](ctx, c, proto.TypeDeviceInvite, proto.DeviceInviteReq{})
	return res.Code, err
}

func (c *Client) CreateAgent(ctx context.Context, handle, displayName string) (string, error) {
	res, err := change[proto.AgentCreateRes](ctx, c, proto.TypeAgentCreate,
		proto.AgentCreateReq{Handle: handle, DisplayName: displayName})
	return res.Code, err
}

func (c *Client) AdminInvite(ctx context.Context, handle, displayName string) (string, error) {
	res, err := change[proto.AdminInviteRes](ctx, c, proto.TypeAdminInvite,
		proto.AdminInviteReq{Handle: handle, DisplayName: displayName})
	return res.Code, err
}
