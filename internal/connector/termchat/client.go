// Package termchat is the connector for a termchat server: SSH transport,
// pinned host key, device-key authentication and the proto frame protocol.
package termchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

const (
	backoffMin     = time.Second
	backoffMax     = 30 * time.Second
	requestTimeout = 10 * time.Second
	// A connection that stays silent through one keepalive interval plus the
	// reply timeout is treated as dead, so a sleeping laptop or a network that
	// drops packets silently still ends in a reconnect and sync.
	keepaliveRequest = "keepalive@termchat"
	keepaliveEvery   = 30 * time.Second
	keepaliveTimeout = 15 * time.Second
	// updatesBuffer matches the server's per-session outbox, so a consumer
	// that briefly stalls does not stall the connection.
	updatesBuffer = 256
)

// Client is a connector.Connector for one termchat account.
type Client struct {
	account config.Account
	addr    string
	dataDir string

	// Replaceable in tests.
	dial    dialFunc
	after   func(time.Duration) <-chan time.Time
	timeout time.Duration

	keepaliveEvery, keepaliveTimeout time.Duration

	updates chan connector.Update

	mu    sync.Mutex
	state connector.ConnState
	me    model.Person
	cur   *conn // non-nil only while connected
}

var _ connector.Connector = (*Client)(nil)

// New returns a Client for account. Keys and the pinned host key live under
// dataDir. Nothing connects until Run is called.
func New(account config.Account, dataDir string) *Client {
	return &Client{
		account: account,
		addr:    normalizeAddr(account.Server),
		dataDir: dataDir,
		dial:    defaultDial,
		after:   time.After,
		timeout: requestTimeout,

		keepaliveEvery:   keepaliveEvery,
		keepaliveTimeout: keepaliveTimeout,
		updates:          make(chan connector.Update, updatesBuffer),
		state:            connector.StateOffline,
		me:               model.Person{Handle: account.Handle},
	}
}

func (c *Client) State() connector.ConnState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) Updates() <-chan connector.Update { return c.updates }

// Me is the signed-in person as the server last reported it; before the first
// connection only Handle is set.
func (c *Client) Me() model.Person {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.me
}

func (c *Client) Caps() connector.Caps {
	return connector.Caps{Edit: true, Delete: true, Rooms: true, Agents: true}
}

// emit queues u for the consumer, waiting while the buffer is full. It
// reports false when ctx ended first.
func (c *Client) emit(ctx context.Context, u connector.Update) bool {
	select {
	case c.updates <- u:
		return true
	case <-ctx.Done():
		return false
	}
}

// setState records s and announces a change with an empty Update.
func (c *Client) setState(ctx context.Context, s connector.ConnState) {
	c.mu.Lock()
	changed := c.state != s
	c.state = s
	c.mu.Unlock()
	if changed {
		c.emit(ctx, connector.Update{})
	}
}

func (c *Client) current() *conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

// Run connects, serves until the connection drops, backs off and redials
// until ctx ends, and then returns nil. In a terminal state it stops dialling
// and waits for ctx.
func (c *Client) Run(ctx context.Context) error {
	delay := backoffMin
	for ctx.Err() == nil {
		c.setState(ctx, connector.StateConnecting)
		terminal, connected := c.session(ctx)
		if ctx.Err() != nil {
			break
		}
		if terminal != "" {
			c.setState(ctx, terminal)
			<-ctx.Done()
			break
		}
		if connected {
			delay = backoffMin
		}
		c.setState(ctx, connector.StateOffline)
		select {
		case <-c.after(delay):
		case <-ctx.Done():
		}
		delay = min(delay*2, backoffMax)
	}
	c.mu.Lock()
	if !c.state.Terminal() {
		c.state = connector.StateOffline
	}
	c.mu.Unlock()
	return nil
}

// session makes one connection attempt and serves it until it ends. A
// non-empty terminal state means no redial may follow; connected reports
// whether hello succeeded, which resets the backoff.
func (c *Client) session(ctx context.Context) (terminal connector.ConnState, connected bool) {
	signer, err := loadKey(c.dataDir, c.addr)
	if errors.Is(err, fs.ErrNotExist) {
		return connector.StateNotAuthorised, false // never joined from this machine
	}
	if err != nil {
		return "", false
	}
	if _, err := pinnedKey(c.dataDir, c.addr); err != nil {
		if errors.Is(err, ErrHostKeyUnpinned) {
			return connector.StateNotAuthorised, false
		}
		return "", false
	}

	var check hostKeyCheck
	cn, hello, err := connect(ctx, c.timeout, c.dial, c.addr, &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: check.callback(c.dataDir, c.addr, false),
	}, c.deliver)
	switch {
	case err == nil:
	case errors.Is(err, ErrHostKeyChanged) || hostKeyRefused(&check):
		return connector.StateHostKeyChanged, false
	case errors.Is(err, connector.ErrVersionMismatch):
		return connector.StateVersionMismatch, false
	case errors.Is(err, connector.ErrNotAuthorised):
		return connector.StateNotAuthorised, false
	default:
		return "", false
	}
	defer cn.close()
	if hello.Person == nil {
		return connector.StateNotAuthorised, false
	}

	c.mu.Lock()
	c.me, c.cur = *hello.Person, cn
	c.mu.Unlock()
	c.setState(ctx, connector.StateConnected)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		c.keepAlive(ctx, cn)
	}()
	select {
	case <-cn.dead:
	case <-ctx.Done():
		cn.close()
		<-cn.dead
	}
	<-stopped
	c.mu.Lock()
	c.cur = nil
	c.mu.Unlock()
	return "", true
}

// keepAlive pings the server every keepaliveEvery and closes cn when no reply
// comes within keepaliveTimeout. The server answers a want-reply request it
// does not know with a failure, and any reply shows the peer is alive. It
// returns once cn is closed or ctx ends.
func (c *Client) keepAlive(ctx context.Context, cn *conn) {
	tick := time.NewTicker(c.keepaliveEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-cn.dead:
			return
		case <-ctx.Done():
			return
		}
		replied := make(chan struct{})
		go func() {
			defer close(replied)
			cn.ssh.SendRequest(keepaliveRequest, true, nil)
		}()
		timer := time.NewTimer(c.keepaliveTimeout)
		select {
		case <-replied: // also fires when the connection closes under the request
			timer.Stop()
		case <-timer.C:
			cn.close()
			<-replied
			return
		case <-cn.dead:
			timer.Stop()
			<-replied
			return
		case <-ctx.Done():
			timer.Stop()
			cn.close()
			<-replied
			return
		}
	}
}

func hostKeyRefused(check *hostKeyCheck) bool {
	_, err := check.result()
	return errors.Is(err, ErrHostKeyChanged)
}

// deliver turns a push frame into an Update. A frame it cannot decode ends the
// connection, so the reconnect resynchronises instead of skipping the update.
func (c *Client) deliver(ctx context.Context, f proto.Frame) bool {
	var u connector.Update
	switch f.Type {
	case proto.TypeEvent:
		var p proto.EventPush
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		u.Event = &p.Event
	case proto.TypeRead:
		var p proto.ReadPush
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		u.Read = &connector.ReadUpdate{ConvID: p.Conv, Seq: p.Seq}
	case proto.TypeConvUpdated:
		var p proto.ConvUpdatedPush
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		u.Conv = &p.Conv
	default:
		return true // a push type this client predates carries nothing to deliver
	}
	return c.emit(ctx, u)
}

// Join registers this machine with the server at the given invite code. It
// creates the device key for the server if missing, pins the server's host
// key on first contact and refuses a server whose key differs from the pin.
// When the device key is already registered the invite is not spent.
func Join(ctx context.Context, server, inviteCode, dataDir string) (config.Account, error) {
	return join(ctx, server, inviteCode, dataDir, defaultDial, requestTimeout)
}

func join(ctx context.Context, server, inviteCode, dataDir string, dial dialFunc, timeout time.Duration) (config.Account, error) {
	addr := normalizeAddr(server)
	signer, err := loadOrCreateKey(dataDir, addr)
	if err != nil {
		return config.Account{}, err
	}
	var check hostKeyCheck
	cn, hello, err := connect(ctx, timeout, dial, addr, &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: check.callback(dataDir, addr, true),
	}, nil)
	if err != nil {
		if _, herr := check.result(); errors.Is(herr, ErrHostKeyChanged) {
			return config.Account{}, fmt.Errorf("%w (%s)", ErrHostKeyChanged, addr)
		}
		if errors.Is(err, connector.ErrNotAuthorised) || errors.Is(err, connector.ErrVersionMismatch) {
			return config.Account{}, err
		}
		return config.Account{}, fmt.Errorf("termchat: connect to %s: %w", addr, err)
	}
	defer cn.close()
	if key, _ := check.result(); key != nil {
		if err := pinKey(dataDir, addr, key); err != nil {
			return config.Account{}, err
		}
	}

	person := hello.Person
	if person == nil {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var res proto.InviteRedeemRes
		if err := cn.request(rctx, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: inviteCode}, &res); err != nil {
			return config.Account{}, err
		}
		person = &res.Person
	}
	return config.Account{Name: person.Handle, Server: addr, Handle: person.Handle}, nil
}
