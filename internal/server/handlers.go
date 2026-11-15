package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

const (
	maxHistoryLimit = 200
	maxMsgIDLen     = 64
	maxMentions     = 50

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

// protoErr builds a response error.
func protoErr(code, msg string) *proto.Error { return &proto.Error{Code: code, Message: msg} }

func internalError() *proto.Error { return protoErr(proto.CodeInternal, "internal error") }

// decode unmarshals a request payload. An empty payload decodes to the zero
// value only when allowEmpty is set; otherwise it is malformed.
func decode[T any](payload []byte, allowEmpty bool) (T, *proto.Error) {
	var v T
	if len(payload) == 0 && allowEmpty {
		return v, nil
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return v, protoErr(proto.CodeInvalid, "malformed payload")
	}
	return v, nil
}

// protoError maps a handler error to a response error. Anything unexpected is
// logged and reported without detail.
func (s *Server) protoError(typ string, err error) *proto.Error {
	var pe *proto.Error
	switch {
	case errors.As(err, &pe):
		return pe
	case errors.Is(err, ErrNotFound):
		return protoErr(proto.CodeNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		return protoErr(proto.CodeForbidden, "not allowed")
	case errors.Is(err, ErrExists):
		return protoErr(proto.CodeInvalid, "already exists")
	case errors.Is(err, ErrLastAdmin):
		return protoErr(proto.CodeInvalid, "that is the last admin")
	case errors.Is(err, ErrInvalidName):
		return protoErr(proto.CodeInvalid, "invalid name")
	case errors.Is(err, ErrInviteInvalid):
		return protoErr(proto.CodeNotAuthorised, "invite not valid")
	}
	s.log.Error("request failed", "type", typ, "err", err)
	return internalError()
}

// register installs fn for typ: it decodes the payload into Req, resolves the
// caller from the store, and maps the returned error to a response error.
func register[Req any](s *Server, typ string, fn func(ctx context.Context, c call, req Req) (any, error)) {
	s.Handle(typ, func(ctx context.Context, sess *Session, f proto.Frame) (any, *proto.Error) {
		req, perr := decode[Req](f.Payload, true)
		if perr != nil {
			return nil, perr
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
	return s.withReadSeq(c, handle)
}

// withReadSeq returns a copy of c carrying handle's read marker.
func (s *Server) withReadSeq(c model.Conversation, handle string) (model.Conversation, error) {
	var err error
	c.ReadSeq, err = s.store.readSeq(handle, c.ID)
	return c, err
}

// publish pushes ev to the conversation's current members plus extra, which
// holds a person who has just left. The caller holds the conversation lock and
// has committed ev. If the member list cannot be read the event stays in the
// log and clients receive it on their next conv.sync.
func (s *Server) publish(ev model.Event, except *Session, extra ...string) {
	members, err := s.store.Members(ev.ConvID)
	if err != nil {
		s.log.Error("publish: load members", "conv", ev.ConvID, "seq", ev.Seq, "err", err)
		return
	}
	s.hub.Publish(ev, append(members, extra...), except)
}

// commit runs op, which writes one event, and publishes the event. The caller
// holds the conversation lock from lockConv, so the write and the publish
// cannot interleave with another writer's and every session sees Seq order.
func (s *Server) commit(except *Session, op func() (model.Event, error), extra ...string) (model.Event, error) {
	ev, err := op()
	if err != nil {
		return ev, err
	}
	s.publish(ev, except, extra...)
	return ev, nil
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
	open, err := s.store.openRoomsWithout(c.me.Handle)
	if err != nil {
		return nil, err
	}
	res := proto.ConvListRes{Mine: mine, OpenRooms: open}
	if res.Mine == nil {
		res.Mine = []model.Conversation{}
	}
	if res.OpenRooms == nil {
		res.OpenRooms = []model.Conversation{}
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
	evs, err := s.store.History(req.Conv, req.BeforeSeq, min(req.Limit, maxHistoryLimit))
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
	mine, err := s.store.convIDsFor(c.me.Handle)
	if err != nil {
		return nil, err
	}
	res := proto.ConvSyncRes{Events: []model.Event{}}
	used, full := 0, false
	for _, id := range slices.Sorted(maps.Keys(req.Cursors)) {
		// Not visibleConv: a cursor for a conversation the caller has left
		// is listed in Gone rather than failing the whole sync.
		if !mine[id] {
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
	_, unlock, err := s.lockConv(c.me, req.Conv, member(c.me))
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
	_, unlock, err := s.lockConv(c.me, req.Conv, member(c.me))
	if err != nil {
		return nil, err
	}
	defer unlock()
	ev, err := s.commit(c.sess, func() (model.Event, error) {
		return s.store.EditMessage(req.Conv, c.me.Handle, req.MsgID, req.Body, req.Mentions)
	})
	if err != nil {
		return nil, err
	}
	return proto.MsgEditRes{Event: ev}, nil
}

func (s *Server) msgDelete(_ context.Context, c call, req proto.MsgDeleteReq) (any, error) {
	_, unlock, err := s.lockConv(c.me, req.Conv, member(c.me))
	if err != nil {
		return nil, err
	}
	defer unlock()
	ev, err := s.commit(c.sess, func() (model.Event, error) {
		return s.store.DeleteMessage(req.Conv, c.me.Handle, req.MsgID)
	})
	if err != nil {
		return nil, err
	}
	return proto.MsgDeleteRes{Event: ev}, nil
}

func (s *Server) readSet(_ context.Context, c call, req proto.ReadSetReq) (any, error) {
	conv, err := s.memberConv(c.me, req.Conv)
	if err != nil {
		return nil, err
	}
	last, err := s.store.lastSeq(conv.ID)
	if err != nil {
		return nil, err
	}
	seq := min(max(req.Seq, 0), last)
	if err := s.store.SetRead(c.me.Handle, req.Conv, seq); err != nil {
		return nil, err
	}
	s.hub.push([]string{c.me.Handle}, proto.TypeRead, proto.ReadPush{Conv: req.Conv, Seq: seq}, c.sess)
	return struct{}{}, nil
}
