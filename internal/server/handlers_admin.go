package server

import (
	"context"
	"errors"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

func (s *Server) deviceInvite(_ context.Context, c call, _ proto.DeviceInviteReq) (any, error) {
	if err := requireHuman(c.me); err != nil {
		return nil, err
	}
	code, err := s.store.CreateInvite(c.me.Handle, c.me.DisplayName, inviteTTL)
	if err != nil {
		return nil, err
	}
	return proto.DeviceInviteRes{Code: code}, nil
}

// requireNewHandle refuses a handle that already belongs to someone: an
// invite for it would let the redeemer's key into that person's account.
func (s *Server) requireNewHandle(raw string) (string, error) {
	h, err := targetHandle(raw)
	if err != nil {
		return "", err
	}
	if _, err := s.store.Person(h); err == nil {
		return "", fail(proto.CodeInvalid, "handle already exists")
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	return h, nil
}

func (s *Server) agentCreate(_ context.Context, c call, req proto.AgentCreateReq) (any, error) {
	if err := requireHuman(c.me); err != nil {
		return nil, err
	}
	h, err := s.requireNewHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	code, err := s.store.CreateAgentInvite(h, req.DisplayName, c.me.Handle, inviteTTL)
	if err != nil {
		return nil, err
	}
	return proto.AgentCreateRes{Code: code}, nil
}

func (s *Server) adminInvite(_ context.Context, c call, req proto.AdminInviteReq) (any, error) {
	if err := requireAdmin(c.me); err != nil {
		return nil, err
	}
	h, err := s.requireNewHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	code, err := s.store.CreateInvite(h, req.DisplayName, inviteTTL)
	if err != nil {
		return nil, err
	}
	return proto.AdminInviteRes{Code: code}, nil
}

// adminRemovePerson takes the person out of every room first, so members see
// member.left, then deletes them and closes their connections.
func (s *Server) adminRemovePerson(_ context.Context, c call, req proto.AdminRemovePersonReq) (any, error) {
	if err := requireAdmin(c.me); err != nil {
		return nil, err
	}
	target, err := targetHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	if target == c.me.Handle {
		return nil, fail(proto.CodeInvalid, "admins cannot remove themselves")
	}
	if _, err := s.store.Person(target); err != nil {
		return nil, err
	}
	keys, err := s.store.Keys(target)
	if err != nil {
		return nil, err
	}
	convs, err := s.store.ConversationsFor(target)
	if err != nil {
		return nil, err
	}
	for _, conv := range convs {
		if conv.Kind != model.ConvRoom {
			continue
		}
		if _, err := s.removeMember(c, conv.ID, target); err != nil {
			return nil, err
		}
	}
	if err := s.store.RemovePerson(target); err != nil {
		return nil, err
	}
	for _, k := range keys {
		s.Kick(k.Fingerprint)
	}
	return struct{}{}, nil
}

func (s *Server) adminRemoveKey(_ context.Context, c call, req proto.AdminRemoveKeyReq) (any, error) {
	if err := requireAdmin(c.me); err != nil {
		return nil, err
	}
	if err := s.store.RemoveKey(req.Fingerprint); err != nil {
		return nil, err
	}
	s.Kick(req.Fingerprint)
	return struct{}{}, nil
}
