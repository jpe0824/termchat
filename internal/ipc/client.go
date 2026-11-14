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

	// submu orders subscribe and unsubscribe requests, so an unsubscribe
	// for an ended subscription can never land after a newer subscribe.
	submu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan frame
	sub     *subscription
	subErr  error
	done    chan struct{}
}

// subscription is one Subscribe channel. The reader never waits on it: an
// event that does not fit ends the subscription instead.
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

// offer queues ev without blocking and reports false when the queue is full.
func (s *subscription) offer(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	select {
	case s.ch <- ev:
		return true
	default:
		return false
	}
}

// detach ends sub, if it is still the client's subscription, recording why;
// unless the connection is gone it then tells the server to stop sending.
func (c *Client) detach(sub *subscription, why error) {
	c.mu.Lock()
	if c.sub != sub {
		c.mu.Unlock()
		return
	}
	c.sub, c.subErr = nil, why
	c.mu.Unlock()
	sub.end()
	if errors.Is(why, ErrClosed) {
		return
	}
	go func() {
		c.submu.Lock()
		defer c.submu.Unlock()
		c.mu.Lock()
		resubscribed := c.sub != nil
		c.mu.Unlock()
		if !resubscribed {
			_ = c.call(opUnsubscribe, nil, nil)
		}
	}()
}

// ErrNotRunning is DialRunning's error when no background process is
// listening.
var ErrNotRunning = errors.New("no background process is running")

// DialRunning connects to the background process and performs the version
// handshake. Unlike Dial it never starts one: when none is listening it
// returns an error matching ErrNotRunning.
func DialRunning(ctx context.Context) (*Client, error) {
	sock, err := SocketPath()
	if err != nil {
		return nil, err
	}
	c, err := dialPath(ctx, sock)
	if err != nil && notRunning(err) {
		return nil, fmt.Errorf("%w: %w", ErrNotRunning, err)
	}
	return c, err
}

// Dial connects to the background process, starting it if none is running,
// and performs the version handshake.
func Dial(ctx context.Context) (*Client, error) {
	c, err := DialRunning(ctx)
	if !errors.Is(err, ErrNotRunning) {
		return c, err
	}
	sock, err := SocketPath()
	if err != nil {
		return nil, err
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

// SubscriptionErr reports why the most recent subscription's channel
// closed: ErrSubscriptionOverflow when its reader fell more than the queue
// behind, ErrClosed when the connection ended, and nil while it is open or
// after its context ended. After an overflow, Subscribe again and re-read
// state; requests on the Client are unaffected.
func (c *Client) SubscriptionErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subErr
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
			if sub != nil && !sub.offer(*f.Event) {
				c.detach(sub, ErrSubscriptionOverflow)
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
	c.mu.Unlock()
	if sub != nil {
		c.detach(sub, ErrClosed)
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

// result runs one request and decodes its result as a T.
func result[T any](c *Client, op string, a *args) (T, error) {
	var out T
	err := c.call(op, a, &out)
	return out, err
}

func (c *Client) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Status reports the account, connection and outbox state.
func (c *Client) Status() (Status, error) {
	return result[Status](c, opStatus, nil)
}

// Chats returns the user's conversations and the open rooms not joined.
func (c *Client) Chats() ([]store.ConvView, []model.Conversation, error) {
	r, err := result[Chats](c, opChats, nil)
	return r.Mine, r.Open, err
}

// Messages returns up to limit messages of conv older than beforeSeq (0: the
// newest, followed by the outbox), oldest first. Older history is fetched
// from the server when the local copy runs out. It never moves the read
// position.
func (c *Client) Messages(conv string, beforeSeq int64, limit int) ([]store.Message, error) {
	return result[[]store.Message](c, opMessages, &args{Conv: conv, BeforeSeq: beforeSeq, Limit: limit})
}

// Outbox returns the user's unsent items of conv, oldest first: those still
// sending and those that failed, with the reason in Err.
func (c *Client) Outbox(conv string) ([]store.OutboxItem, error) {
	return result[[]store.OutboxItem](c, opOutbox, &args{Conv: conv})
}

// Send queues text for conv and returns its message id at once; delivery
// is reported through Subscribe. source is "tui" or "cli".
func (c *Client) Send(conv, text, source string) (string, error) {
	return result[string](c, opSend, &args{Conv: conv, Text: text, Source: source})
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
	return result[[]store.Message](c, opSearch, &args{Query: q, Limit: limit})
}

// Resolve finds a conversation by "@handle" (the DM), "#room", id, or bare
// name. It returns ErrNotFound or ErrAmbiguous.
func (c *Client) Resolve(ref string) (model.Conversation, error) {
	return result[model.Conversation](c, opResolve, &args{Ref: ref})
}

// OpenDM returns the DM with handle, creating it on the server if needed.
func (c *Client) OpenDM(handle string) (model.Conversation, error) {
	return result[model.Conversation](c, opOpenDM, &args{Handle: handle})
}

// Room runs one room operation.
func (c *Client) Room(op connector.RoomOp) (model.Conversation, error) {
	return result[model.Conversation](c, opRoom, &args{Room: &op})
}

// DeviceInvite returns a code that adds another device to this account.
func (c *Client) DeviceInvite() (string, error) {
	return result[string](c, opDeviceInvite, nil)
}

// CreateAgent creates an agent identity owned by the user and returns its
// invite code.
func (c *Client) CreateAgent(handle, name string) (string, error) {
	return result[string](c, opCreateAgent, &args{Handle: handle, Name: name})
}

// AdminInvite creates an invite for a new person (admins only).
func (c *Client) AdminInvite(handle, name string) (string, error) {
	return result[string](c, opAdminInvite, &args{Handle: handle, Name: name})
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
// subscription ends, when the channel closes; SubscriptionErr says why. The
// channel holds a bounded number of events: a consumer that falls further
// behind loses the subscription (ErrSubscriptionOverflow) rather than
// stalling the Client, and should Subscribe again and re-read state. One
// subscription per Client at a time.
func (c *Client) Subscribe(ctx context.Context) (<-chan Event, error) {
	return c.subscribe(ctx, false, false)
}

// SubscribeUI is Subscribe for a client that shows conversations to the user.
// The background process counts the conversation reported with SetFocus as
// being looked at. canNotify says whether this UI can show a notification
// when sent a notify event; notify events go only to UIs that can, and when
// none is attached the background process uses another route.
func (c *Client) SubscribeUI(ctx context.Context, canNotify bool) (<-chan Event, error) {
	return c.subscribe(ctx, true, canNotify)
}

func (c *Client) subscribe(ctx context.Context, ui, canNotify bool) (<-chan Event, error) {
	c.submu.Lock()
	defer c.submu.Unlock()
	sub := &subscription{ch: make(chan Event, eventsBuffer), done: make(chan struct{})}
	c.mu.Lock()
	if c.sub != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: already subscribed", ErrInvalid)
	}
	c.sub, c.subErr = sub, nil
	c.mu.Unlock()
	if err := c.call(opSubscribe, &args{UI: ui, CanNotify: canNotify}, nil); err != nil {
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
			c.detach(sub, nil)
		case <-sub.done:
		}
	}()
	return sub.ch, nil
}
