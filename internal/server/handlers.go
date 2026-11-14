package server

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

const (
	maxHistoryLimit = 200
	maxMsgIDLen     = 64
	maxMentions     = 50
	inviteTTL       = InviteTTL

	// responseBudget bounds the events in one history or sync response so a
	// frame of maximum-size bodies stays under proto.MaxFrame.
	responseBudget = 512 << 10
	// syncPageEvents bounds how many events one cursor loads.
	syncPageEvents = 1000
)

// call is what a handler knows about one request.
type call struct {
	sess *Session
	me   model.Person
}

func internalError() *proto.Error {
	return &proto.Error{Code: proto.CodeInternal, Message: "internal error"}
}

// protoError maps a handler error to a response error. Anything unexpected is
// logged and reported without detail.
func (s *Server) protoError(typ string, err error) *proto.Error {
	var pe *proto.Error
	switch {
	case errors.As(err, &pe):
		return pe
	case errors.Is(err, ErrNotFound):
		return &proto.Error{Code: proto.CodeNotFound, Message: "not found"}
	case errors.Is(err, ErrForbidden):
		return &proto.Error{Code: proto.CodeForbidden, Message: "not allowed"}
	case errors.Is(err, ErrExists):
		return &proto.Error{Code: proto.CodeInvalid, Message: "already exists"}
	case errors.Is(err, ErrLastAdmin):
		return &proto.Error{Code: proto.CodeInvalid, Message: "that is the last admin"}
	case errors.Is(err, ErrInvalidName):
		return &proto.Error{Code: proto.CodeInvalid, Message: "invalid name"}
	case errors.Is(err, ErrInviteInvalid):
		return &proto.Error{Code: proto.CodeNotAuthorised, Message: "invite not valid"}
	}
	s.log.Error("request failed", "type", typ, "err", err)
	return internalError()
}

// register installs fn for typ: it decodes the payload into Req, resolves the
// caller from the store, and maps the returned error to a response error.
func register[Req any](s *Server, typ string, fn func(ctx context.Context, c call, req Req) (any, error)) {
	s.Handle(typ, func(ctx context.Context, sess *Session, f proto.Frame) (any, *proto.Error) {
		var req Req
		if len(f.Payload) > 0 {
			if err := json.Unmarshal(f.Payload, &req); err != nil {
				return nil, &proto.Error{Code: proto.CodeInvalid, Message: "malformed payload"}
			}
		}
		me, err := s.caller(sess)
		if err != nil {
			return nil, s.protoError(typ, err)
		}
		res, err := fn(ctx, call{sess: sess, me: me}, req)
		if err != nil {
			return nil, s.protoError(typ, err)
		}
		return res, nil
	})
}

func (s *Server) registerHandlers() {
	register(s, proto.TypePeopleList, s.peopleList)
	register(s, proto.TypeConvList, s.convList)
	register(s, proto.TypeConvHistory, s.convHistory)
	register(s, proto.TypeConvSync, s.convSync)
	register(s, proto.TypeMsgSend, s.msgSend)
	register(s, proto.TypeMsgEdit, s.msgEdit)
	register(s, proto.TypeMsgDelete, s.msgDelete)
	register(s, proto.TypeReadSet, s.readSet)
	register(s, proto.TypeDMOpen, s.dmOpen)
	register(s, proto.TypeRoomCreate, s.roomCreate)
	register(s, proto.TypeRoomJoin, s.roomJoin)
	register(s, proto.TypeRoomLeave, s.roomLeave)
	register(s, proto.TypeRoomInvite, s.roomInvite)
	register(s, proto.TypeRoomRemove, s.roomRemove)
	register(s, proto.TypeRoomRename, s.roomRename)
	register(s, proto.TypeDeviceInvite, s.deviceInvite)
	register(s, proto.TypeAgentCreate, s.agentCreate)
	register(s, proto.TypeAdminInvite, s.adminInvite)
	register(s, proto.TypeAdminRemovePerson, s.adminRemovePerson)
	register(s, proto.TypeAdminRemoveKey, s.adminRemoveKey)
}

// convFor is the conversation as handle sees it, with their read marker.
func (s *Server) convFor(id, handle string) (model.Conversation, error) {
	c, err := s.store.Conversation(id)
	if err != nil {
		return c, err
	}
	c.ReadSeq, err = s.store.readSeq(handle, id)
	return c, err
}

// publish pushes ev to the conversation's current members plus extra, which
// holds a person who has just left. The caller holds the conversation lock and
// has committed ev. If the member list cannot be read the event stays in the
// log and clients receive it on their next conv.sync.
func (s *Server) publish(ev model.Event, except *Session, extra ...string) {
	c, err := s.store.Conversation(ev.ConvID)
	if err != nil {
		s.log.Error("publish: load members", "conv", ev.ConvID, "seq", ev.Seq, "err", err)
		return
	}
	s.hub.Publish(ev, append(c.Members, extra...), except)
}

// lockMember takes the conversation lock for a member's write. Membership is
// checked again under the lock so a removal that completed first wins.
func (s *Server) lockMember(p model.Person, conv string) (func(), error) {
	if _, err := s.memberConv(p, conv); err != nil {
		return nil, err
	}
	unlock := s.hub.lock(conv)
	if err := s.requireMember(conv, p.Handle); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// ---- reading ----

func (s *Server) peopleList(_ context.Context, _ call, _ proto.PeopleListReq) (any, error) {
	people, err := s.store.People()
	if err != nil {
		return nil, err
	}
	if people == nil {
		people = []model.Person{}
	}
	return proto.PeopleListRes{People: people}, nil
}

func (s *Server) convList(_ context.Context, c call, _ proto.ConvListReq) (any, error) {
	mine, err := s.store.ConversationsFor(c.me.Handle)
	if err != nil {
		return nil, err
	}
	open, err := s.store.OpenRooms()
	if err != nil {
		return nil, err
	}
	res := proto.ConvListRes{Mine: mine, OpenRooms: []model.Conversation{}}
	if res.Mine == nil {
		res.Mine = []model.Conversation{}
	}
	for _, r := range open {
		if !isMember(r, c.me.Handle) {
			res.OpenRooms = append(res.OpenRooms, r)
		}
	}
	return res, nil
}

func eventSize(ev model.Event) int {
	b, _ := json.Marshal(ev)
	return len(b) + 1
}

func (s *Server) convHistory(_ context.Context, c call, req proto.ConvHistoryReq) (any, error) {
	if _, err := s.memberConv(c.me, req.Conv); err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	evs, err := s.store.History(req.Conv, req.BeforeSeq, limit)
	if err != nil {
		return nil, err
	}
	// Keep the newest events that fit; the client pages back with BeforeSeq.
	start, used := len(evs), 0
	for start > 0 {
		size := eventSize(evs[start-1])
		if used+size > responseBudget && start < len(evs) {
			break
		}
		used += size
		start--
	}
	evs = evs[start:]
	if evs == nil {
		evs = []model.Event{}
	}
	return proto.ConvHistoryRes{Events: evs}, nil
}

func (s *Server) convSync(_ context.Context, c call, req proto.ConvSyncReq) (any, error) {
	convs := make([]string, 0, len(req.Cursors))
	for id := range req.Cursors {
		convs = append(convs, id)
	}
	sort.Strings(convs)

	res := proto.ConvSyncRes{Events: []model.Event{}}
	used, full := 0, false
	for _, id := range convs {
		ok, err := s.store.IsMember(id, c.me.Handle)
		if err != nil {
			return nil, err
		}
		if !ok {
			res.Gone = append(res.Gone, id)
			continue
		}
		limit := syncPageEvents
		if full {
			limit = 1 // only to learn whether anything is held back
		}
		evs, err := s.store.afterLimit(id, req.Cursors[id], limit)
		if err != nil {
			return nil, err
		}
		if full {
			res.More = res.More || len(evs) > 0
			continue
		}
		for _, ev := range evs {
			size := eventSize(ev)
			if used+size > responseBudget && len(res.Events) > 0 {
				full, res.More = true, true
				break
			}
			used += size
			res.Events = append(res.Events, ev)
		}
		if !full && len(evs) == syncPageEvents {
			res.More = true
		}
	}
	return res, nil
}

// ---- messages ----

func validMentions(in []string) error {
	if len(in) > maxMentions {
		return fail(proto.CodeInvalid, "too many mentions")
	}
	for _, m := range in {
		if !model.ValidName(model.NormalizeName(m)) {
			return fail(proto.CodeInvalid, "invalid mention")
		}
	}
	return nil
}

func (s *Server) msgSend(_ context.Context, c call, req proto.MsgSendReq) (any, error) {
	if req.MsgID == "" || len(req.MsgID) > maxMsgIDLen {
		return nil, fail(proto.CodeInvalid, "msg_id must be 1 to 64 bytes")
	}
	if err := validMentions(req.Mentions); err != nil {
		return nil, err
	}
	unlock, err := s.lockMember(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ev, dup, err := s.store.PostMessage(req.Conv, c.me.Handle, req.MsgID, req.Body, req.Mentions)
	if err != nil {
		return nil, err
	}
	if dup {
		// The id is unique per conversation, not per sender: another
		// member's retry must not reveal their message to the caller.
		if ev.Actor != c.me.Handle {
			return nil, fail(proto.CodeForbidden, "msg_id already used")
		}
		return proto.MsgSendRes{Event: ev, Duplicate: true}, nil
	}
	s.publish(ev, c.sess)
	return proto.MsgSendRes{Event: ev}, nil
}

func (s *Server) msgEdit(_ context.Context, c call, req proto.MsgEditReq) (any, error) {
	if err := validMentions(req.Mentions); err != nil {
		return nil, err
	}
	unlock, err := s.lockMember(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ev, err := s.store.EditMessage(req.Conv, c.me.Handle, req.MsgID, req.Body, req.Mentions)
	if err != nil {
		return nil, err
	}
	s.publish(ev, c.sess)
	return proto.MsgEditRes{Event: ev}, nil
}

func (s *Server) msgDelete(_ context.Context, c call, req proto.MsgDeleteReq) (any, error) {
	unlock, err := s.lockMember(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ev, err := s.store.DeleteMessage(req.Conv, c.me.Handle, req.MsgID)
	if err != nil {
		return nil, err
	}
	s.publish(ev, c.sess)
	return proto.MsgDeleteRes{Event: ev}, nil
}

func (s *Server) readSet(_ context.Context, c call, req proto.ReadSetReq) (any, error) {
	if _, err := s.memberConv(c.me, req.Conv); err != nil {
		return nil, err
	}
	conv, err := s.store.Conversation(req.Conv)
	if err != nil {
		return nil, err
	}
	seq := min(max(req.Seq, 0), conv.LastSeq)
	if err := s.store.SetRead(c.me.Handle, req.Conv, seq); err != nil {
		return nil, err
	}
	s.hub.push([]string{c.me.Handle}, proto.TypeRead, proto.ReadPush{Conv: req.Conv, Seq: seq}, c.sess)
	return struct{}{}, nil
}
