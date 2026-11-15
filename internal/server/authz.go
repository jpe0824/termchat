package server

import (
	"errors"
	"slices"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// Each predicate returns nil when the action is allowed. They take the caller
// as stored now, not as it was when the session started, so a demoted or
// removed person loses access on the next request.

func fail(code, msg string) error { return protoErr(code, msg) }

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

// visibleConv loads a conversation, without LastSeq, that the caller may know exists: one they
// belong to, an open room, or any room when the caller is an admin. Every
// other conversation, including a missing id, answers with the same
// not_found, so a reply never reveals a hidden private room or DM. Handlers
// that take a conversation id load it only through this or memberConv.
func (s *Server) visibleConv(p model.Person, id string) (model.Conversation, error) {
	conv, err := s.store.conversation(id)
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
	return conv, requireMember(conv, p.Handle)
}

// lockConv takes the conversation's lock, then loads it and authorises the
// caller: visibility first, then each guard in order. Deciding under the lock
// means a removal that completes first always wins. On success the caller
// must call unlock.
//
// An id with no conversation is refused before the lock is taken, so made-up
// ids cannot grow the never-pruned lock map. Conversations are never deleted,
// so that check cannot go stale.
func (s *Server) lockConv(p model.Person, id string, guards ...func(model.Conversation) error) (conv model.Conversation, unlock func(), err error) {
	ok, err := s.store.convExists(id)
	if err != nil {
		return conv, nil, err
	}
	if !ok {
		return conv, nil, notVisible()
	}
	unlock = s.hub.lock(id)
	conv, err = s.visibleConv(p, id)
	for _, g := range guards {
		if err != nil {
			break
		}
		err = g(conv)
	}
	if err != nil {
		unlock()
		return conv, nil, err
	}
	return conv, unlock, nil
}

// member, manager and joinable bind a caller to a lockConv guard.
func member(p model.Person) func(model.Conversation) error {
	return func(c model.Conversation) error { return requireMember(c, p.Handle) }
}

func manager(p model.Person) func(model.Conversation) error {
	return func(c model.Conversation) error { return requireManager(c, p) }
}

func joinable(p model.Person) func(model.Conversation) error {
	return func(c model.Conversation) error {
		if isMember(c, p.Handle) {
			return fail(proto.CodeInvalid, "already a member")
		}
		if !c.Open {
			return fail(proto.CodeForbidden, "room is private")
		}
		return nil
	}
}

func requireMember(c model.Conversation, handle string) error {
	if !isMember(c, handle) {
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

func isMember(c model.Conversation, handle string) bool { return slices.Contains(c.Members, handle) }
