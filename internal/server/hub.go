package server

import (
	"encoding/json"
	"sync"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// Hub fans events out to the live sessions of the people they concern.
//
// Pushes are queued on each session, never written by the publisher, so a
// slow or stalled client cannot delay a request or another recipient.
// Callers that must keep Seq order hold the conversation lock from the store
// commit through Publish; queueing preserves that order on every session.
type Hub struct {
	srv   *Server
	locks sync.Map // conversation id -> *sync.Mutex
}

func newHub(s *Server) *Hub { return &Hub{srv: s} }

// Hub returns the server's fan-out hub.
func (s *Server) Hub() *Hub { return s.hub }

// lock serialises commit-and-publish for one conversation and returns the
// unlock function.
func (h *Hub) lock(conv string) func() {
	m, _ := h.locks.LoadOrStore(conv, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Publish pushes ev to every live known session of each listed member except
// except, which already has the event in its response. The sender's other
// devices do receive it.
func (h *Hub) Publish(ev model.Event, members []string, except *Session) {
	h.push(members, proto.TypeEvent, proto.EventPush{Event: ev}, except)
}

// push queues one frame for every live known session of the listed people.
func (h *Hub) push(handles []string, typ string, payload any, except *Session) {
	b, err := json.Marshal(payload)
	if err != nil {
		h.srv.log.Error("encode push", "type", typ, "err", err)
		return
	}
	want := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		want[handle] = struct{}{}
	}
	var targets []*Session
	h.srv.mu.Lock()
	for sess := range h.srv.sessions {
		if sess == except || sess.ctx.Err() != nil {
			continue
		}
		if p, known := sess.Identity(); known {
			if _, ok := want[p.Handle]; ok {
				targets = append(targets, sess)
			}
		}
	}
	h.srv.mu.Unlock()
	for _, sess := range targets {
		sess.enqueue(typ, b)
	}
}
