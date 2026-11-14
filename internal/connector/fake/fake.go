// Package fake is an in-memory connector.Connector for tests. It behaves like
// a well-mannered server: per-conversation dense Seq, MsgID idempotency and
// ErrOffline while not connected. It is deterministic and does no I/O.
package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
)

const (
	updatesBuffer = 1024
	defaultLimit  = 50
)

var epoch = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// Fake implements connector.Connector in memory.
type Fake struct {
	updates chan connector.Update

	mu       sync.Mutex
	state    connector.ConnState
	me       model.Person
	people   []model.Person
	convs    map[string]*model.Conversation
	events   map[string][]model.Event
	sent     []model.Event
	failSend error
	pageSize int
	clock    int
	nextConv int
	nextCode int
}

var _ connector.Connector = (*Fake)(nil)

// New returns a connected Fake acting as me.
func New(me model.Person) *Fake {
	return &Fake{
		updates: make(chan connector.Update, updatesBuffer),
		state:   connector.StateConnected,
		me:      me,
		people:  []model.Person{me},
		convs:   map[string]*model.Conversation{},
		events:  map[string][]model.Event{},
	}
}

// Run blocks until ctx ends.
func (f *Fake) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (f *Fake) State() connector.ConnState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *Fake) Updates() <-chan connector.Update { return f.updates }

func (f *Fake) Me() model.Person {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.me
}

func (f *Fake) Caps() connector.Caps {
	return connector.Caps{Edit: true, Delete: true, Rooms: true, Agents: true}
}

// SetState changes the state and announces it with an empty Update.
func (f *Fake) SetState(s connector.ConnState) {
	f.mu.Lock()
	changed := f.state != s
	f.state = s
	f.mu.Unlock()
	if changed {
		f.updates <- connector.Update{}
	}
}

// FailNextSend makes the next Send return err without recording anything.
func (f *Fake) FailNextSend(err error) {
	f.mu.Lock()
	f.failSend = err
	f.mu.Unlock()
}

// Sent returns the events created by Send, oldest first. A duplicate send
// does not add an entry.
func (f *Fake) Sent() []model.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// SetPageSize caps how many events one History or Sync call returns; zero
// removes the cap.
func (f *Fake) SetPageSize(n int) {
	f.mu.Lock()
	f.pageSize = n
	f.mu.Unlock()
}

// AddPeople seeds the people list.
func (f *Fake) AddPeople(p ...model.Person) {
	f.mu.Lock()
	f.people = append(f.people, p...)
	f.mu.Unlock()
}

// AddConversation seeds a conversation and its events, which keep the Seq
// they carry or take the next free one. A conversation with no members gets
// the caller as its only member.
func (f *Fake) AddConversation(c model.Conversation, events ...model.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(c.Members) == 0 {
		c.Members = []string{f.me.Handle}
	}
	f.convs[c.ID] = &c
	for _, ev := range events {
		ev.ConvID = c.ID
		f.appendLocked(ev)
	}
}

// Push delivers ev on Updates as if another member acted. It takes the next
// Seq when ev.Seq is zero and is stored, so History and Sync return it.
func (f *Fake) Push(ev model.Event) {
	f.mu.Lock()
	ev = f.appendLocked(ev)
	f.mu.Unlock()
	f.updates <- connector.Update{Event: &ev}
}

// appendLocked stores ev, filling Seq and At, and keeps LastSeq current.
func (f *Fake) appendLocked(ev model.Event) model.Event {
	if ev.Seq == 0 {
		ev.Seq = int64(len(f.events[ev.ConvID])) + 1
	}
	if ev.At.IsZero() {
		f.clock++
		ev.At = epoch.Add(time.Duration(f.clock) * time.Second)
	}
	f.events[ev.ConvID] = append(f.events[ev.ConvID], ev)
	if c := f.convs[ev.ConvID]; c != nil {
		c.LastSeq = max(c.LastSeq, ev.Seq)
	}
	return ev
}

// ready returns ErrOffline unless connected. Callers hold f.mu.
func (f *Fake) ready() error {
	if f.state != connector.StateConnected {
		return connector.ErrOffline
	}
	return nil
}

// member returns the conversation when the caller belongs to it. Callers hold f.mu.
func (f *Fake) member(id string) (*model.Conversation, error) {
	c := f.convs[id]
	if c == nil || !slices.Contains(c.Members, f.me.Handle) {
		return nil, fmt.Errorf("%w: conversation %s", connector.ErrNotFound, id)
	}
	return c, nil
}

func (f *Fake) People(context.Context) ([]model.Person, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return nil, err
	}
	return slices.Clone(f.people), nil
}

func (f *Fake) Conversations(context.Context) (mine, openRooms []model.Conversation, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return nil, nil, err
	}
	for _, id := range slices.Sorted(maps.Keys(f.convs)) {
		c := *f.convs[id]
		switch {
		case slices.Contains(c.Members, f.me.Handle):
			mine = append(mine, c)
		case c.Kind == model.ConvRoom && c.Open:
			openRooms = append(openRooms, c)
		}
	}
	return mine, openRooms, nil
}

func (f *Fake) History(_ context.Context, conv string, beforeSeq int64, limit int) ([]model.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return nil, err
	}
	if _, err := f.member(conv); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if f.pageSize > 0 {
		limit = min(limit, f.pageSize)
	}
	var older []model.Event
	for _, ev := range f.events[conv] {
		if beforeSeq == 0 || ev.Seq < beforeSeq {
			older = append(older, ev)
		}
	}
	if len(older) > limit {
		older = older[len(older)-limit:]
	}
	return slices.Clone(older), nil
}

func (f *Fake) Sync(_ context.Context, cursors map[string]int64) (events []model.Event, more bool, gone []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return nil, false, nil, err
	}
	for _, id := range slices.Sorted(maps.Keys(cursors)) {
		if _, err := f.member(id); err != nil {
			gone = append(gone, id)
			continue
		}
		for _, ev := range f.events[id] {
			if ev.Seq <= cursors[id] {
				continue
			}
			if f.pageSize > 0 && len(events) == f.pageSize {
				more = true
				break
			}
			events = append(events, ev)
		}
	}
	return events, more, gone, nil
}

func (f *Fake) Send(_ context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return model.Event{}, err
	}
	if err := f.failSend; err != nil {
		f.failSend = nil
		return model.Event{}, err
	}
	if _, err := f.member(conv); err != nil {
		return model.Event{}, err
	}
	for _, ev := range f.events[conv] {
		if ev.Kind == model.MessagePosted && ev.MsgID == msgID {
			return ev, nil
		}
	}
	ev := f.appendLocked(model.Event{
		ConvID: conv, Kind: model.MessagePosted, Actor: f.me.Handle,
		MsgID: msgID, Body: &body, Mentions: mentions,
	})
	f.sent = append(f.sent, ev)
	return ev, nil
}

// ownMessage finds the caller's posted message. Callers hold f.mu.
func (f *Fake) ownMessage(conv, msgID string) error {
	if _, err := f.member(conv); err != nil {
		return err
	}
	for _, ev := range f.events[conv] {
		if ev.Kind == model.MessagePosted && ev.MsgID == msgID {
			if ev.Actor != f.me.Handle {
				return fmt.Errorf("%w: not your message", connector.ErrForbidden)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: message %s", connector.ErrNotFound, msgID)
}

func (f *Fake) Edit(_ context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return model.Event{}, err
	}
	if err := f.ownMessage(conv, msgID); err != nil {
		return model.Event{}, err
	}
	return f.appendLocked(model.Event{
		ConvID: conv, Kind: model.MessageEdited, Actor: f.me.Handle,
		MsgID: msgID, Body: &body, Mentions: mentions,
	}), nil
}

func (f *Fake) Delete(_ context.Context, conv, msgID string) (model.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return model.Event{}, err
	}
	if err := f.ownMessage(conv, msgID); err != nil {
		return model.Event{}, err
	}
	return f.appendLocked(model.Event{
		ConvID: conv, Kind: model.MessageDeleted, Actor: f.me.Handle, MsgID: msgID,
	}), nil
}

// MarkRead moves the read marker forward only.
func (f *Fake) MarkRead(_ context.Context, conv string, seq int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return err
	}
	c, err := f.member(conv)
	if err != nil {
		return err
	}
	c.ReadSeq = max(c.ReadSeq, min(seq, c.LastSeq))
	return nil
}

func (f *Fake) OpenDM(_ context.Context, handle string) (model.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return model.Conversation{}, err
	}
	if !slices.ContainsFunc(f.people, func(p model.Person) bool { return p.Handle == handle }) {
		return model.Conversation{}, fmt.Errorf("%w: person %s", connector.ErrNotFound, handle)
	}
	pair := []string{f.me.Handle, handle}
	slices.Sort(pair)
	id := "dm:" + pair[0] + ":" + pair[1]
	if c := f.convs[id]; c != nil {
		return *c, nil
	}
	c := &model.Conversation{ID: id, Kind: model.ConvDM, Members: pair}
	f.convs[id] = c
	return *c, nil
}

func (f *Fake) Room(_ context.Context, op connector.RoomOp) (model.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return model.Conversation{}, err
	}
	switch op.Kind {
	case connector.RoomCreate, connector.RoomJoin, connector.RoomLeave,
		connector.RoomInvite, connector.RoomRemove, connector.RoomRename:
	default:
		return model.Conversation{}, fmt.Errorf("%w: unknown room operation %q", connector.ErrInvalid, op.Kind)
	}
	if op.Kind == connector.RoomCreate {
		f.nextConv++
		c := &model.Conversation{
			ID: fmt.Sprintf("room-%d", f.nextConv), Kind: model.ConvRoom, Name: op.Name,
			Open: op.Open, Creator: f.me.Handle, Members: []string{f.me.Handle},
		}
		f.convs[c.ID] = c
		return *c, nil
	}
	c := f.convs[op.Conv]
	if c == nil || c.Kind != model.ConvRoom {
		return model.Conversation{}, fmt.Errorf("%w: room %s", connector.ErrNotFound, op.Conv)
	}
	if op.Kind == connector.RoomJoin {
		if !c.Open && !slices.Contains(c.Members, f.me.Handle) {
			return model.Conversation{}, fmt.Errorf("%w: room is not open", connector.ErrForbidden)
		}
		f.addMember(c, f.me.Handle)
		return *c, nil
	}
	if !slices.Contains(c.Members, f.me.Handle) {
		return model.Conversation{}, fmt.Errorf("%w: not a member", connector.ErrForbidden)
	}
	switch op.Kind {
	case connector.RoomLeave:
		f.dropMember(c, f.me.Handle)
	case connector.RoomInvite:
		f.addMember(c, op.Handle)
	case connector.RoomRemove:
		f.dropMember(c, op.Handle)
	case connector.RoomRename:
		c.Name = op.Name
		f.appendLocked(model.Event{ConvID: c.ID, Kind: model.RoomRenamed, Actor: f.me.Handle, Name: op.Name})
	}
	return *c, nil
}

func (f *Fake) addMember(c *model.Conversation, handle string) {
	if slices.Contains(c.Members, handle) {
		return
	}
	c.Members = append(c.Members, handle)
	f.appendLocked(model.Event{ConvID: c.ID, Kind: model.MemberJoined, Actor: f.me.Handle, Target: handle})
}

func (f *Fake) dropMember(c *model.Conversation, handle string) {
	if !slices.Contains(c.Members, handle) {
		return
	}
	f.appendLocked(model.Event{ConvID: c.ID, Kind: model.MemberLeft, Actor: f.me.Handle, Target: handle})
	c.Members = slices.DeleteFunc(c.Members, func(m string) bool { return m == handle })
}

// invite returns the next deterministic code for a permitted caller.
func (f *Fake) invite(allowed bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(); err != nil {
		return "", err
	}
	if !allowed {
		return "", connector.ErrForbidden
	}
	f.nextCode++
	return fmt.Sprintf("invite-%d", f.nextCode), nil
}

func (f *Fake) DeviceInvite(context.Context) (string, error) {
	return f.invite(!f.Me().Agent)
}

func (f *Fake) CreateAgent(context.Context, string, string) (string, error) {
	return f.invite(!f.Me().Agent)
}

func (f *Fake) AdminInvite(context.Context, string, string) (string, error) {
	return f.invite(f.Me().Admin)
}
