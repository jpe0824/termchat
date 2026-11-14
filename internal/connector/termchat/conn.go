package termchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/proto"
)

const (
	channelType      = "termchat"
	sshUser          = "termchat"
	handshakeTimeout = 10 * time.Second
)

// dialFunc opens the SSH connection. Tests replace it to count attempts.
type dialFunc func(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error)

// defaultDial bounds the TCP connect and the SSH handshake by cfg.Timeout and
// abandons both when ctx ends.
func defaultDial(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: cfg.Timeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()
	nc.SetDeadline(time.Now().Add(cfg.Timeout))
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

// conn is one live connection: one goroutine reads frames, routes responses
// to waiting requests by ID and hands pushes to deliver.
type conn struct {
	ssh   *ssh.Client
	codec *proto.Codec

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan proto.Frame

	dead      chan struct{} // closed when the reader has exited
	closeOnce sync.Once
}

// deliver receives each push frame in order. It returns false to end the
// connection, which it does when ctx is done or the frame cannot be used.
type deliver func(ctx context.Context, f proto.Frame) bool

// connect runs the whole connection sequence (TCP dial, SSH handshake,
// channel open and hello) under ctx and one timeout, so a peer that goes
// silent at any step cannot hold the caller. ctx also governs pushes on the
// connection once it is established; the timeout does not. A nil handler
// discards pushes.
func connect(ctx context.Context, timeout time.Duration, dial dialFunc, addr string, cfg *ssh.ClientConfig, handler deliver) (*conn, proto.HelloRes, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cfg.User = sshUser
	cfg.Timeout = handshakeTimeout
	wrap := func(err error) error {
		if cerr := cctx.Err(); cerr != nil {
			return fmt.Errorf("termchat: connect to %s: %w", addr, cerr)
		}
		return err
	}

	client, err := dial(cctx, addr, cfg)
	if err != nil {
		return nil, proto.HelloRes{}, wrap(err)
	}
	// The dial's own closer ends with the handshake, so keep one until hello
	// has answered.
	stop := context.AfterFunc(cctx, func() { client.Close() })
	ch, reqs, err := client.OpenChannel(channelType, nil)
	if err != nil {
		stop()
		client.Close()
		return nil, proto.HelloRes{}, wrap(err)
	}
	go ssh.DiscardRequests(reqs)
	cn := &conn{
		ssh:     client,
		codec:   proto.NewCodec(ch),
		pending: map[uint64]chan proto.Frame{},
		dead:    make(chan struct{}),
	}
	go cn.readLoop(ctx, handler)

	var hello proto.HelloRes
	err = cn.request(cctx, proto.TypeHello, proto.HelloReq{Version: proto.Version}, &hello)
	if err == nil && !stop() {
		err = cctx.Err() // the closer already fired
	}
	if err != nil {
		stop()
		cn.close()
		<-cn.dead
		return nil, proto.HelloRes{}, wrap(err)
	}
	return cn, hello, nil
}

func (cn *conn) close() {
	cn.closeOnce.Do(func() { cn.ssh.Close() })
}

func (cn *conn) readLoop(ctx context.Context, handler deliver) {
	defer close(cn.dead)
	defer cn.close()
	for {
		f, err := cn.codec.Read()
		if err != nil {
			return
		}
		if f.ID != 0 {
			cn.mu.Lock()
			ch := cn.pending[f.ID]
			delete(cn.pending, f.ID)
			cn.mu.Unlock()
			if ch != nil {
				ch <- f // capacity 1, one response per ID
			}
			continue
		}
		if handler != nil && !handler(ctx, f) {
			return
		}
	}
}

// inFlight marks an error from a request whose frame was written, so the
// server may have acted on it.
type inFlight struct{ err error }

func (e *inFlight) Error() string { return e.err.Error() }
func (e *inFlight) Unwrap() error { return e.err }

// request sends one frame and decodes the response payload into res (when
// non-nil). It returns when the response arrives, ctx ends or the connection
// drops, in which case the error is ErrOffline.
func (cn *conn) request(ctx context.Context, typ string, req, res any) error {
	f := proto.Frame{Type: typ}
	if req != nil {
		b, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("termchat: encode %s: %w", typ, err)
		}
		f.Payload = b
	}
	ch := make(chan proto.Frame, 1)
	cn.mu.Lock()
	cn.nextID++
	f.ID = cn.nextID
	cn.pending[f.ID] = ch
	cn.mu.Unlock()
	defer func() {
		cn.mu.Lock()
		delete(cn.pending, f.ID)
		cn.mu.Unlock()
	}()

	if err := cn.codec.Write(f); err != nil {
		if errors.Is(err, proto.ErrFrameTooLarge) {
			return fmt.Errorf("%w: request exceeds the frame limit", connector.ErrTooLarge)
		}
		return offline(err)
	}
	select {
	case resp := <-ch:
		return decodeResponse(resp, res)
	case <-cn.dead:
		select {
		case resp := <-ch: // answered just before the connection dropped
			return decodeResponse(resp, res)
		default:
			return &inFlight{offline(nil)}
		}
	case <-ctx.Done():
		return &inFlight{ctx.Err()}
	}
}

func offline(cause error) error {
	if cause == nil {
		return connector.ErrOffline
	}
	return fmt.Errorf("%w: %v", connector.ErrOffline, cause)
}

func decodeResponse(f proto.Frame, res any) error {
	if f.Error != nil {
		return remoteError(f.Error)
	}
	if res == nil || len(f.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(f.Payload, res); err != nil {
		return fmt.Errorf("%w: malformed %s response: %v", connector.ErrInternal, f.Type, err)
	}
	return nil
}

// remoteError maps a protocol error to its sentinel, keeping the server's
// message in the text.
func remoteError(e *proto.Error) error {
	var sentinel error
	switch e.Code {
	case proto.CodeNotAuthorised:
		sentinel = connector.ErrNotAuthorised
	case proto.CodeForbidden:
		sentinel = connector.ErrForbidden
	case proto.CodeNotFound:
		sentinel = connector.ErrNotFound
	case proto.CodeInvalid:
		sentinel = connector.ErrInvalid
	case proto.CodeTooLarge:
		sentinel = connector.ErrTooLarge
	case proto.CodeRateLimited:
		sentinel = connector.ErrRateLimited
	case proto.CodeVersionMismatch:
		sentinel = connector.ErrVersionMismatch
	case proto.CodeInternal:
		sentinel = connector.ErrInternal
	default:
		return fmt.Errorf("%w: %s", connector.ErrInternal, e.Error())
	}
	if e.Message == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, e.Message)
}
