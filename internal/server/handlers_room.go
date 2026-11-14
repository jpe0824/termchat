package server

import (
	"context"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// loadRoom loads a conversation that must be a room.
func (s *Server) loadRoom(p model.Person, id string) (model.Conversation, error) {
	conv, err := s.visibleConv(p, id)
	if err != nil {
		return conv, err
	}
	return conv, requireRoom(conv)
}

// targetHandle normalises a handle from a request.
func targetHandle(raw string) (string, error) {
	h := model.NormalizeName(raw)
	if !model.ValidName(h) {
		return "", fail(proto.CodeInvalid, "invalid handle")
	}
	return h, nil
}

func (s *Server) dmOpen(_ context.Context, c call, req proto.DMOpenReq) (any, error) {
	other, err := targetHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	dm, err := s.store.OpenDM(c.me.Handle, other)
	if err != nil {
		return nil, err
	}
	theirs, err := s.convFor(dm.ID, other)
	if err != nil {
		return nil, err
	}
	s.hub.push([]string{other}, proto.TypeConvUpdated, proto.ConvUpdatedPush{Conv: theirs}, nil)
	mine, err := s.convFor(dm.ID, c.me.Handle)
	if err != nil {
		return nil, err
	}
	return proto.DMOpenRes{Conv: mine}, nil
}

// roomCreate tells the creator's other devices through conv.updated rather
// than an event push: no one else can be in the room yet, and the join event
// is in the history those devices fetch.
func (s *Server) roomCreate(_ context.Context, c call, req proto.RoomCreateReq) (any, error) {
	conv, err := s.store.CreateRoom(req.Name, req.Open, c.me.Handle)
	if err != nil {
		return nil, err
	}
	s.hub.push([]string{c.me.Handle}, proto.TypeConvUpdated, proto.ConvUpdatedPush{Conv: conv}, c.sess)
	return proto.RoomCreateRes{Conv: conv}, nil
}

func (s *Server) roomJoin(_ context.Context, c call, req proto.RoomJoinReq) (any, error) {
	conv, err := s.loadRoom(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	if isMember(conv, c.me.Handle) {
		return nil, fail(proto.CodeInvalid, "already a member")
	}
	if !conv.Open {
		return nil, fail(proto.CodeForbidden, "room is private")
	}
	unlock := s.hub.lock(conv.ID)
	defer unlock()
	ev, err := s.store.AddMember(conv.ID, c.me.Handle, c.me.Handle)
	if err != nil {
		return nil, err
	}
	return s.announceJoin(c, ev, c.me.Handle)
}

// announceJoin pushes a member.joined event: the new member's devices first
// learn the conversation, then every member receives the event. It returns the
// conversation as the caller now sees it. The caller holds the lock.
func (s *Server) announceJoin(c call, ev model.Event, joined string) (proto.RoomJoinRes, error) {
	theirs, err := s.convFor(ev.ConvID, joined)
	if err != nil {
		return proto.RoomJoinRes{}, err
	}
	except := c.sess
	if joined != c.me.Handle {
		except = nil
	}
	s.hub.push([]string{joined}, proto.TypeConvUpdated, proto.ConvUpdatedPush{Conv: theirs}, except)
	s.publish(ev, c.sess)
	mine, err := s.convFor(ev.ConvID, c.me.Handle)
	return proto.RoomJoinRes{Conv: mine}, err
}

func (s *Server) roomLeave(_ context.Context, c call, req proto.RoomLeaveReq) (any, error) {
	conv, err := s.loadRoom(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	if !isMember(conv, c.me.Handle) {
		return nil, fail(proto.CodeForbidden, "not a member")
	}
	if _, err := s.removeMember(c, conv.ID, c.me.Handle); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// removeMember removes target and publishes member.left to the remaining
// members and to target, whose last push for the room it is.
func (s *Server) removeMember(c call, conv, target string) (model.Event, error) {
	unlock := s.hub.lock(conv)
	defer unlock()
	ev, err := s.store.RemoveMember(conv, c.me.Handle, target)
	if err != nil {
		return ev, err
	}
	s.publish(ev, c.sess, target)
	return ev, nil
}

func (s *Server) roomInvite(_ context.Context, c call, req proto.RoomInviteReq) (any, error) {
	conv, err := s.loadRoom(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	if err := requireManager(conv, c.me); err != nil {
		return nil, err
	}
	target, err := targetHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	unlock := s.hub.lock(conv.ID)
	defer unlock()
	ev, err := s.store.AddMember(conv.ID, c.me.Handle, target)
	if err != nil {
		return nil, err
	}
	res, err := s.announceJoin(c, ev, target)
	if err != nil {
		return nil, err
	}
	return proto.RoomInviteRes(res), nil
}

func (s *Server) roomRemove(_ context.Context, c call, req proto.RoomRemoveReq) (any, error) {
	conv, err := s.loadRoom(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	target, err := targetHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	if target == c.me.Handle {
		// Removing yourself is leaving, open to any member; the reply
		// carries no member list because the caller no longer belongs.
		if !isMember(conv, target) {
			return nil, fail(proto.CodeForbidden, "not a member")
		}
		if _, err := s.removeMember(c, conv.ID, target); err != nil {
			return nil, err
		}
		return proto.RoomRemoveRes{Conv: model.Conversation{ID: conv.ID, Kind: conv.Kind, Name: conv.Name, Open: conv.Open}}, nil
	}
	if err := requireManager(conv, c.me); err != nil {
		return nil, err
	}
	if _, err := s.removeMember(c, conv.ID, target); err != nil {
		return nil, err
	}
	mine, err := s.convFor(conv.ID, c.me.Handle)
	return proto.RoomRemoveRes{Conv: mine}, err
}

func (s *Server) roomRename(_ context.Context, c call, req proto.RoomRenameReq) (any, error) {
	conv, err := s.loadRoom(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	if err := requireManager(conv, c.me); err != nil {
		return nil, err
	}
	unlock := s.hub.lock(conv.ID)
	defer unlock()
	ev, err := s.store.RenameRoom(conv.ID, c.me.Handle, req.Name)
	if err != nil {
		return nil, err
	}
	s.publish(ev, c.sess)
	mine, err := s.convFor(conv.ID, c.me.Handle)
	return proto.RoomRenameRes{Conv: mine}, err
}
