package server

import (
	"errors"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// Each predicate returns nil when the action is allowed. They take the caller
// as stored now, not as it was when the session started, so a demoted or
// removed person loses access on the next request.

func fail(code, msg string) error { return &proto.Error{Code: code, Message: msg} }

// caller loads the session's person from the store.
func (s *Server) caller(sess *Session) (model.Person, error) {
	p, known := sess.Identity()
	if !known {
		return p, fail(proto.CodeNotAuthorised, "unknown key")
	}
	fresh, err := s.store.Person(p.Handle)
	if errors.Is(err, ErrNotFound) {
		return p, fail(proto.CodeNotAuthorised, "unknown identity")
	}
	return fresh, err
}

func notVisible() error { return fail(proto.CodeNotFound, "not found") }

// visibleConv loads a conversation the caller may know exists: one they
// belong to, an open room, or any room when the caller is an admin. Every
// other conversation, including a missing id, answers with the same
// not_found, so a reply never reveals a hidden private room or DM. Handlers
// that take a conversation id load it only through this or memberConv.
func (s *Server) visibleConv(p model.Person, id string) (model.Conversation, error) {
	conv, err := s.store.Conversation(id)
	if errors.Is(err, ErrNotFound) {
		return conv, notVisible()
	}
	if err != nil {
		return conv, err
	}
	room := conv.Kind == model.ConvRoom
	if isMember(conv, p.Handle) || (room && conv.Open) || (room && p.Admin) {
		return conv, nil
	}
	return conv, notVisible()
}

// memberConv is visibleConv for requests only members may make; a visible
// conversation the caller is not in answers forbidden.
func (s *Server) memberConv(p model.Person, id string) (model.Conversation, error) {
	conv, err := s.visibleConv(p, id)
	if err != nil {
		return conv, err
	}
	if !isMember(conv, p.Handle) {
		return conv, fail(proto.CodeForbidden, "not a member")
	}
	return conv, nil
}

// requireMember re-checks membership, for use under the conversation lock.
func (s *Server) requireMember(conv, handle string) error {
	ok, err := s.store.IsMember(conv, handle)
	if err != nil {
		return err
	}
	if !ok {
		return fail(proto.CodeForbidden, "not a member")
	}
	return nil
}

func requireAdmin(p model.Person) error {
	if !p.Admin {
		return fail(proto.CodeForbidden, "admin only")
	}
	return nil
}

// requireHuman refuses agent accounts, which may not mint invites or agents.
func requireHuman(p model.Person) error {
	if p.Agent {
		return fail(proto.CodeForbidden, "not available to agent accounts")
	}
	return nil
}

// requireRoom refuses direct messages: they have no room membership to manage.
func requireRoom(c model.Conversation) error {
	if c.Kind != model.ConvRoom {
		return fail(proto.CodeInvalid, "not a room")
	}
	return nil
}

// requireManager allows an admin, or the room's creator while still a member:
// a creator who left or was removed has no standing in the room.
func requireManager(c model.Conversation, p model.Person) error {
	if p.Admin || (c.Creator != "" && c.Creator == p.Handle && isMember(c, p.Handle)) {
		return nil
	}
	return fail(proto.CodeForbidden, "only the room's creator or an admin may do that")
}

func isMember(c model.Conversation, handle string) bool {
	for _, m := range c.Members {
		if m == handle {
			return true
		}
	}
	return false
}
