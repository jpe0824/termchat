package server

import (
	"context"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

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
	theirs, err := s.withReadSeq(dm, other)
	if err != nil {
		return nil, err
	}
	s.hub.push([]string{other}, proto.TypeConvUpdated, proto.ConvUpdatedPush{Conv: theirs}, nil)
	mine, err := s.withReadSeq(dm, c.me.Handle)
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
	conv, unlock, err := s.lockConv(c.me, req.Conv, requireRoom, joinable(c.me))
	if err != nil {
		return nil, err
	}
	defer unlock()
	mine, err := s.announceJoin(c, conv.ID, c.me.Handle, c.sess)
	return proto.RoomJoinRes{Conv: mine}, err
}

// announceJoin adds target as the caller and tells everyone: target's devices
// other than except first learn the conversation, then every member receives
// member.joined. It returns the conversation as the caller now sees it. The
// caller holds the lock.
func (s *Server) announceJoin(c call, id, target string, except *Session) (model.Conversation, error) {
	var conv model.Conversation
	_, err := s.commit(c.sess, func() (model.Event, error) {
		ev, err := s.store.AddMember(id, c.me.Handle, target)
		if err != nil {
			return ev, err
		}
		if conv, err = s.store.Conversation(id); err != nil {
			return ev, err
		}
		theirs, err := s.withReadSeq(conv, target)
		if err != nil {
			return ev, err
		}
		s.hub.push([]string{target}, proto.TypeConvUpdated, proto.ConvUpdatedPush{Conv: theirs}, except)
		return ev, nil
	})
	if err != nil {
		return conv, err
	}
	return s.withReadSeq(conv, c.me.Handle)
}

func (s *Server) roomLeave(_ context.Context, c call, req proto.RoomLeaveReq) (any, error) {
	conv, unlock, err := s.lockConv(c.me, req.Conv, requireRoom)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.leaveRoom(c, conv); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// leaveRoom takes the caller out of the room, which any member may do. The
// caller holds the lock.
func (s *Server) leaveRoom(c call, conv model.Conversation) error {
	if err := requireMember(conv, c.me.Handle); err != nil {
		return err
	}
	_, err := s.removeMember(c, conv.ID, c.me.Handle)
	return err
}

// removeMember removes target and publishes member.left to the remaining
// members and to target, whose last push for the room it is. The caller holds
// the lock.
func (s *Server) removeMember(c call, conv, target string) (model.Event, error) {
	return s.commit(c.sess, func() (model.Event, error) {
		return s.store.RemoveMember(conv, c.me.Handle, target)
	}, target)
}

func (s *Server) roomInvite(_ context.Context, c call, req proto.RoomInviteReq) (any, error) {
	conv, unlock, err := s.lockConv(c.me, req.Conv, requireRoom, manager(c.me))
	if err != nil {
		return nil, err
	}
	defer unlock()
	target, err := targetHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	// Inviting yourself is joining: the requesting device gets the
	// conversation in its response instead of a push.
	var except *Session
	if target == c.me.Handle {
		except = c.sess
	}
	mine, err := s.announceJoin(c, conv.ID, target, except)
	if err != nil {
		return nil, err
	}
	return proto.RoomInviteRes{Conv: mine}, nil
}

func (s *Server) roomRemove(_ context.Context, c call, req proto.RoomRemoveReq) (any, error) {
	conv, unlock, err := s.lockConv(c.me, req.Conv, requireRoom)
	if err != nil {
		return nil, err
	}
	defer unlock()
	target, err := targetHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	if target == c.me.Handle {
		// The reply carries no member list because the caller no longer
		// belongs.
		if err := s.leaveRoom(c, conv); err != nil {
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
	conv, unlock, err := s.lockConv(c.me, req.Conv, requireRoom, manager(c.me))
	if err != nil {
		return nil, err
	}
	defer unlock()
	_, err = s.commit(c.sess, func() (model.Event, error) {
		return s.store.RenameRoom(conv.ID, c.me.Handle, req.Name)
	})
	if err != nil {
		return nil, err
	}
	mine, err := s.convFor(conv.ID, c.me.Handle)
	return proto.RoomRenameRes{Conv: mine}, err
}
