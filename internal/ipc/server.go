package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// queueLen bounds the frames waiting to be written to one connection. A
// subscriber that falls this far behind is disconnected rather than allowed
// to hold up the background process; it reconnects and re-reads state.
const queueLen = 1024

// shutdownWait bounds how long Close waits for running requests; tests
// shorten it.
var shutdownWait = 5 * time.Second

// flushTimeout bounds how long a closing connection may take to write the
// frames already queued for it.
const flushTimeout = time.Second

// Backend serves the requests arriving on the socket. Its methods are called
// concurrently, each on its own goroutine, with a context that ends when the
// server closes.
type Backend interface {
	Status(ctx context.Context) (Status, error)
	Chats(ctx context.Context) (Chats, error)
	Messages(ctx context.Context, conv string, beforeSeq int64, limit int) ([]store.Message, error)
	Send(ctx context.Context, conv, text, source string) (string, error)
	Edit(ctx context.Context, conv, msgID, text string) error
	Delete(ctx context.Context, conv, msgID string) error
	Retry(ctx context.Context, msgID string) error
	Discard(ctx context.Context, msgID string) error
	MarkRead(ctx context.Context, conv string) error
	Search(ctx context.Context, q string, limit int) ([]store.Message, error)
	Resolve(ctx context.Context, ref string) (model.Conversation, error)
	OpenDM(ctx context.Context, handle string) (model.Conversation, error)
	Room(ctx context.Context, op connector.RoomOp) (model.Conversation, error)
	DeviceInvite(ctx context.Context) (string, error)
	CreateAgent(ctx context.Context, handle, name string) (string, error)
	AdminInvite(ctx context.Context, handle, name string) (string, error)
	Join(ctx context.Context, server, code string) error
	// Stop asks the process to shut down. It must not block: the server
	// is closed by whoever owns it, after the reply has been queued.
	Stop()
}

// Subscriber is what one subscribed connection last reported with SetFocus.
type Subscriber struct {
	Conv    string
	Focused bool
}

// Server accepts connections and dispatches their requests to a Backend.
type Server struct {
	b   Backend
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // connection readers and writers
	reqs   sync.WaitGroup // requests that have started
	once   sync.Once

	mu    sync.Mutex
	ln    net.Listener
	conns map[*sconn]struct{}
	// closed is set when shutdown begins; from then on no request starts.
	closed bool
}

// NewServer returns a server for b; call Serve to start accepting.
func NewServer(b Backend, log *slog.Logger) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{b: b, log: log, ctx: ctx, cancel: cancel, conns: map[*sconn]struct{}{}}
}

// Serve accepts connections on ln until Close; it then returns nil.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ln.Close()
	}
	s.ln = ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		s.start(c)
	}
}

// Close shuts the server down. From the moment it is called no request
// starts: each is refused with ErrShuttingDown and has no effect. Requests
// already started run to completion, waited for up to shutdownWait, and
// their replies are written before each connection closes. A request still
// running after that has its context cancelled and its reply is dropped.
// Close may be called more than once.
func (s *Server) Close() {
	s.once.Do(s.shutdown)
	s.wg.Wait()
}

func (s *Server) shutdown() {
	s.mu.Lock()
	s.closed = true
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.reqs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
		s.log.Warn("requests still running at shutdown; abandoning them")
	}
	s.cancel()

	s.mu.Lock()
	conns := make([]*sconn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.close(true)
	}
}

// begin registers a request about to start, or reports that shutdown has
// begun. Checking and counting under one lock means Close never misses a
// request that started.
func (s *Server) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.reqs.Add(1)
	return true
}

func (s *Server) shuttingDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Broadcast queues ev for every subscribed connection without waiting. A
// connection whose queue is full is closed.
func (s *Server) Broadcast(ev Event) {
	b, err := json.Marshal(frame{Event: &ev})
	if err != nil {
		s.log.Error("encode event", "kind", ev.Kind, "err", err)
		return
	}
	b = append(b, '\n')
	s.mu.Lock()
	var subs []*sconn
	for c := range s.conns {
		if c.isSubscribed() {
			subs = append(subs, c)
		}
	}
	s.mu.Unlock()
	for _, c := range subs {
		c.queue(b)
	}
}

// Subscribers lists the subscribed connections.
func (s *Server) Subscribers() []Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Subscriber
	for c := range s.conns {
		c.mu.Lock()
		if c.subscribed {
			out = append(out, c.focus)
		}
		c.mu.Unlock()
	}
	return out
}

type sconn struct {
	s    *Server
	c    net.Conn
	out  chan []byte
	done chan struct{}
	once sync.Once

	mu         sync.Mutex
	hello      bool
	subscribed bool
	focus      Subscriber
}

func (s *Server) start(c net.Conn) {
	sc := &sconn{s: s, c: c, out: make(chan []byte, queueLen), done: make(chan struct{})}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = c.Close()
		return
	}
	s.conns[sc] = struct{}{}
	s.wg.Add(2)
	s.mu.Unlock()
	go sc.writeLoop()
	go sc.readLoop()
}

func (c *sconn) isSubscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscribed
}

// close ends the connection. With flush, frames already queued are written
// first, bounded by flushTimeout; without, the connection drops at once.
func (c *sconn) close(flush bool) {
	c.once.Do(func() {
		c.s.mu.Lock()
		delete(c.s.conns, c)
		c.s.mu.Unlock()
		c.mu.Lock()
		c.subscribed = false
		c.mu.Unlock()
		if flush {
			_ = c.c.SetWriteDeadline(time.Now().Add(flushTimeout))
		} else {
			_ = c.c.Close()
		}
		close(c.done)
	})
}

// queue hands b to the writer, closing the connection when its queue is full.
func (c *sconn) queue(b []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.out <- b:
	default:
		c.s.log.Warn("closing a client that fell behind")
		c.close(false)
	}
}

func (c *sconn) writeLoop() {
	defer c.s.wg.Done()
	defer func() { _ = c.c.Close() }()
	for {
		select {
		case b := <-c.out:
			if _, err := c.c.Write(b); err != nil {
				c.close(false)
				return
			}
		case <-c.done:
			for {
				select {
				case b := <-c.out:
					if _, err := c.c.Write(b); err != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (c *sconn) readLoop() {
	defer c.s.wg.Done()
	defer c.close(false)
	dec := json.NewDecoder(c.c)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			return
		}
		var a args
		if req.Args != nil {
			a = *req.Args
		}
		if !c.s.begin() {
			c.reply(req.ID, nil, fmt.Errorf("%w; %q was not run", ErrShuttingDown, req.Op))
			continue
		}
		c.handle(req, a)
	}
}

// handle runs one request that begin has counted, and marks it done once
// its reply is queued.
func (c *sconn) handle(req request, a args) {
	c.mu.Lock()
	hello := c.hello
	c.mu.Unlock()
	if !hello {
		defer c.s.reqs.Done()
		if req.Op != opHello || a.Version != Version {
			c.reply(req.ID, nil, fmt.Errorf("%w: it speaks version %d, the client %d",
				ErrVersionMismatch, Version, a.Version))
			c.close(true)
			return
		}
		c.mu.Lock()
		c.hello = true
		c.mu.Unlock()
		c.reply(req.ID, struct{ Version int }{Version}, nil)
		return
	}
	switch req.Op {
	case opSubscribe, opUnsubscribe, opSetFocus:
		// Handled in order, so events follow the subscribe reply.
		defer c.s.reqs.Done()
		c.mu.Lock()
		switch req.Op {
		case opSubscribe:
			c.subscribed = true
		case opUnsubscribe:
			c.subscribed = false
		case opSetFocus:
			c.focus = Subscriber{Conv: a.Conv, Focused: a.Focused}
		}
		c.mu.Unlock()
		c.reply(req.ID, nil, nil)
	case opStop:
		c.reply(req.ID, nil, nil)
		c.s.reqs.Done() // before Stop: the shutdown it triggers waits for started requests
		c.s.b.Stop()
	default:
		go func() {
			defer c.s.reqs.Done()
			res, err := dispatch(c.s.ctx, c.s.b, req.Op, a)
			c.reply(req.ID, res, err)
		}()
	}
}

func (c *sconn) reply(id uint64, res any, err error) {
	f := frame{ID: id, Error: encodeError(err)}
	if err == nil && res != nil {
		b, merr := json.Marshal(res)
		if merr != nil {
			f.Error = encodeError(merr)
		} else {
			f.Result = b
		}
	}
	b, merr := json.Marshal(f)
	if merr != nil {
		c.s.log.Error("encode reply", "err", merr)
		return
	}
	c.queue(append(b, '\n'))
}

func dispatch(ctx context.Context, b Backend, op string, a args) (any, error) {
	switch op {
	case opStatus:
		return b.Status(ctx)
	case opChats:
		return b.Chats(ctx)
	case opMessages:
		return b.Messages(ctx, a.Conv, a.BeforeSeq, a.Limit)
	case opSend:
		return b.Send(ctx, a.Conv, a.Text, a.Source)
	case opEdit:
		return nil, b.Edit(ctx, a.Conv, a.MsgID, a.Text)
	case opDelete:
		return nil, b.Delete(ctx, a.Conv, a.MsgID)
	case opRetry:
		return nil, b.Retry(ctx, a.MsgID)
	case opDiscard:
		return nil, b.Discard(ctx, a.MsgID)
	case opMarkRead:
		return nil, b.MarkRead(ctx, a.Conv)
	case opSearch:
		return b.Search(ctx, a.Query, a.Limit)
	case opResolve:
		return b.Resolve(ctx, a.Ref)
	case opOpenDM:
		return b.OpenDM(ctx, a.Handle)
	case opRoom:
		if a.Room == nil {
			return nil, fmt.Errorf("%w: room operation missing", ErrInvalid)
		}
		return b.Room(ctx, *a.Room)
	case opDeviceInvite:
		return b.DeviceInvite(ctx)
	case opCreateAgent:
		return b.CreateAgent(ctx, a.Handle, a.Name)
	case opAdminInvite:
		return b.AdminInvite(ctx, a.Handle, a.Name)
	case opJoin:
		return nil, b.Join(ctx, a.Server, a.Code)
	}
	return nil, fmt.Errorf("%w: unknown operation %q", ErrInvalid, op)
}
