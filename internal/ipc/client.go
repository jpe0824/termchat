package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	// startWait bounds how long Dial waits for a process it started.
	startWait = 3 * time.Second
	// requestTimeout bounds one request. Fetching older history may take
	// several server round trips, so it is generous.
	requestTimeout = 90 * time.Second
	eventsBuffer   = 256
)

// clientVersion is the version this client announces; tests change it.
var clientVersion = Version

// StartDaemon starts the background process when Dial finds none listening.
// The default executes the current binary with "daemon run", detached from
// the caller's terminal and process group. Tests replace it.
var StartDaemon = startProcess

// Client is one connection to the background process. Its methods are safe
// for concurrent use.
type Client struct {
	conn net.Conn

	wmu sync.Mutex
	enc *json.Encoder

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan frame
	sub     *subscription
	done    chan struct{}
}

type subscription struct {
	ch     chan Event
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

// end closes the channel once no event is being handed to it.
func (s *subscription) end() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
}

func (s *subscription) deliver(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
	case <-s.done:
	}
}

// Dial connects to the background process, starting it if none is running,
// and performs the version handshake.
func Dial(ctx context.Context) (*Client, error) {
	sock, err := SocketPath()
	if err != nil {
		return nil, err
	}
	c, err := dialPath(ctx, sock)
	if err == nil || !notRunning(err) {
		return c, err
	}
	if err := StartDaemon(ctx); err != nil {
		return nil, fmt.Errorf("start background process: %w", err)
	}
	deadline := time.Now().Add(startWait)
	for {
		c, err = dialPath(ctx, sock)
		if err == nil || !notRunning(err) || time.Now().After(deadline) {
			return c, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// notRunning reports whether a dial failed because nothing is listening.
func notRunning(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT)
}

func dialPath(ctx context.Context, sock string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn: conn, enc: json.NewEncoder(conn),
		pending: map[uint64]chan frame{}, done: make(chan struct{}),
	}
	go c.readLoop()
	var res struct{ Version int }
	if err := c.call(opHello, &args{Version: clientVersion}, &res); err != nil {
		_ = c.Close()
		return nil, err
	}
	if res.Version != clientVersion {
		_ = c.Close()
		return nil, fmt.Errorf("%w: it speaks version %d, the client %d", ErrVersionMismatch, res.Version, clientVersion)
	}
	return c, nil
}

// Close ends the connection; an active subscription's channel closes.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) readLoop() {
	dec := json.NewDecoder(c.conn)
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			break
		}
		if f.Event != nil {
			c.mu.Lock()
			sub := c.sub
			c.mu.Unlock()
			if sub != nil {
				sub.deliver(*f.Event)
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[f.ID]
		delete(c.pending, f.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- f
		}
	}
	_ = c.conn.Close()
	c.mu.Lock()
	close(c.done)
	sub := c.sub
	c.sub = nil
	c.mu.Unlock()
	if sub != nil {
		sub.end()
	}
}

func (c *Client) call(op string, a *args, out any) error {
	ch := make(chan frame, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return ErrClosed
	default:
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	c.wmu.Lock()
	err := c.enc.Encode(request{ID: id, Op: op, Args: a})
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	select {
	case f := <-ch:
		if f.Error != nil {
			return decodeError(f.Error)
		}
		if out != nil && len(f.Result) > 0 {
			return json.Unmarshal(f.Result, out)
		}
		return nil
	case <-c.done:
		return ErrClosed
	case <-timer.C:
		c.forget(id)
		return fmt.Errorf("%w: no answer from the background process", ErrTimeout)
	}
}

func (c *Client) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Status reports the account, connection and outbox state.
func (c *Client) Status() (Status, error) {
	var s Status
	err := c.call(opStatus, nil, &s)
	return s, err
}

// Chats returns the user's conversations and the open rooms not joined.
func (c *Client) Chats() ([]store.ConvView, []model.Conversation, error) {
	var r Chats
	err := c.call(opChats, nil, &r)
	return r.Mine, r.Open, err
}

// Messages returns up to limit messages of conv older than beforeSeq (0: the
// newest, followed by the outbox), oldest first. Older history is fetched
// from the server when the local copy runs out. It never moves the read
// position.
func (c *Client) Messages(conv string, beforeSeq int64, limit int) ([]store.Message, error) {
	var out []store.Message
	err := c.call(opMessages, &args{Conv: conv, BeforeSeq: beforeSeq, Limit: limit}, &out)
	return out, err
}

// Send queues text for conv and returns its message id at once; delivery
// is reported through Subscribe. source is "tui" or "cli".
func (c *Client) Send(conv, text, source string) (string, error) {
	var id string
	err := c.call(opSend, &args{Conv: conv, Text: text, Source: source}, &id)
	return id, err
}

// Edit replaces the text of one of the user's messages.
func (c *Client) Edit(conv, msgID, text string) error {
	return c.call(opEdit, &args{Conv: conv, MsgID: msgID, Text: text}, nil)
}

// Delete removes one of the user's messages.
func (c *Client) Delete(conv, msgID string) error {
	return c.call(opDelete, &args{Conv: conv, MsgID: msgID}, nil)
}

// Retry requeues a failed outbox item.
func (c *Client) Retry(msgID string) error {
	return c.call(opRetry, &args{MsgID: msgID}, nil)
}

// Discard removes an outbox item without sending it.
func (c *Client) Discard(msgID string) error {
	return c.call(opDiscard, &args{MsgID: msgID}, nil)
}

// MarkRead marks conv read up to its newest message.
func (c *Client) MarkRead(conv string) error {
	return c.call(opMarkRead, &args{Conv: conv}, nil)
}

// Search returns messages containing every word of q, newest first.
func (c *Client) Search(q string, limit int) ([]store.Message, error) {
	var out []store.Message
	err := c.call(opSearch, &args{Query: q, Limit: limit}, &out)
	return out, err
}

// Resolve finds a conversation by "@handle" (the DM), "#room", id, or bare
// name. It returns ErrNotFound or ErrAmbiguous.
func (c *Client) Resolve(ref string) (model.Conversation, error) {
	var conv model.Conversation
	err := c.call(opResolve, &args{Ref: ref}, &conv)
	return conv, err
}

// OpenDM returns the DM with handle, creating it on the server if needed.
func (c *Client) OpenDM(handle string) (model.Conversation, error) {
	var conv model.Conversation
	err := c.call(opOpenDM, &args{Handle: handle}, &conv)
	return conv, err
}

// Room runs one room operation.
func (c *Client) Room(op connector.RoomOp) (model.Conversation, error) {
	var conv model.Conversation
	err := c.call(opRoom, &args{Room: &op}, &conv)
	return conv, err
}

// DeviceInvite returns a code that adds another device to this account.
func (c *Client) DeviceInvite() (string, error) {
	var code string
	err := c.call(opDeviceInvite, nil, &code)
	return code, err
}

// CreateAgent creates an agent identity owned by the user and returns its
// invite code.
func (c *Client) CreateAgent(handle, name string) (string, error) {
	var code string
	err := c.call(opCreateAgent, &args{Handle: handle, Name: name}, &code)
	return code, err
}

// AdminInvite creates an invite for a new person (admins only).
func (c *Client) AdminInvite(handle, name string) (string, error) {
	var code string
	err := c.call(opAdminInvite, &args{Handle: handle, Name: name}, &code)
	return code, err
}

// Join registers this machine with a server using an invite code and brings
// the account up.
func (c *Client) Join(server, code string) error {
	return c.call(opJoin, &args{Server: server, Code: code}, nil)
}

// SetFocus reports which conversation this client shows and whether its
// window has focus.
func (c *Client) SetFocus(conv string, focused bool) error {
	return c.call(opSetFocus, &args{Conv: conv, Focused: focused}, nil)
}

// Stop shuts the background process down.
func (c *Client) Stop() error {
	return c.call(opStop, nil, nil)
}

// Subscribe streams events on this connection until ctx ends or the
// connection closes, when the channel closes. The caller must keep reading:
// a subscriber that falls far behind is disconnected, and should reconnect
// and re-read state. One subscription per Client.
func (c *Client) Subscribe(ctx context.Context) (<-chan Event, error) {
	sub := &subscription{ch: make(chan Event, eventsBuffer), done: make(chan struct{})}
	c.mu.Lock()
	if c.sub != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: already subscribed", ErrInvalid)
	}
	c.sub = sub
	c.mu.Unlock()
	if err := c.call(opSubscribe, nil, nil); err != nil {
		c.mu.Lock()
		if c.sub == sub {
			c.sub = nil
		}
		c.mu.Unlock()
		sub.end()
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-sub.done:
			return
		}
		c.mu.Lock()
		if c.sub == sub {
			c.sub = nil
		}
		c.mu.Unlock()
		sub.end()
		_ = c.call(opUnsubscribe, nil, nil)
	}()
	return sub.ch, nil
}
