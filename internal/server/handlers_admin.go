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
	code, err := s.store.CreateDeviceInvite(c.me.Handle, InviteTTL)
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
	code, err := s.store.CreateAgentInvite(h, req.DisplayName, c.me.Handle, InviteTTL)
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
	code, err := s.store.CreateInvite(h, req.DisplayName, InviteTTL)
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
	res, err := RemovePersonFully(s.store, c.me.Handle, target, func(conv string, op func() (model.Event, error)) error {
		unlock := s.hub.lock(conv)
		defer unlock()
		_, err := s.commit(c.sess, op, target)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, fp := range res.Keys {
		s.Kick(fp)
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
