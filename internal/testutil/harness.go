// Package testutil holds helpers shared by tests that need a running server.
package testutil

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
)

// Harness is an in-process server on a loopback port with a temporary data
// directory.
type Harness struct {
	Server *server.Server
	Store  *server.Store

	cancel context.CancelFunc
	done   chan struct{}
	err    error

	mu      sync.Mutex
	clients []*ssh.Client
}

// StartServer starts a server with default limits and stops it when the test
// ends.
func StartServer(t *testing.T) *Harness {
	t.Helper()
	return StartServerWith(t, nil)
}

// StartServerWith is StartServer with a chance to adjust the options first.
func StartServerWith(t *testing.T, adjust func(*server.Options)) *Harness {
	t.Helper()
	opts := server.Options{Addr: "127.0.0.1:0", DataDir: t.TempDir()}
	if adjust != nil {
		adjust(&opts)
	}
	srv, err := server.New(opts)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Harness{Server: srv, Store: srv.Store(), cancel: cancel, done: make(chan struct{})}
	go func() {
		h.err = srv.Serve(ctx)
		close(h.done)
	}()
	t.Cleanup(func() {
		if err := h.Stop(); err != nil {
			t.Errorf("Serve returned %v", err)
		}
	})
	return h
}

// Stop cancels the server and waits for Serve to return, closing any clients
// the harness opened. It returns Serve's result and is safe to call twice.
func (h *Harness) Stop() error {
	h.cancel()
	<-h.done
	h.mu.Lock()
	for _, c := range h.clients {
		c.Close()
	}
	h.clients = nil
	h.mu.Unlock()
	return h.err
}

// NewKey returns a fresh ed25519 signer that the store does not know.
func NewKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// NewPerson creates the person and a fresh key for them directly in the store
// and returns the key.
func (h *Harness) NewPerson(t *testing.T, handle string) ssh.Signer {
	t.Helper()
	signer := NewKey(t)
	if err := h.Store.CreatePerson(model.Person{Handle: handle, DisplayName: handle}); err != nil {
		t.Fatalf("CreatePerson(%q): %v", handle, err)
	}
	pub := signer.PublicKey()
	if err := h.Store.AddKey(handle, ssh.FingerprintSHA256(pub), pub.Marshal()); err != nil {
		t.Fatalf("AddKey(%q): %v", handle, err)
	}
	return signer
}

// SSHClient opens an SSH connection authenticated with key, without opening
// any channel.
func (h *Harness) SSHClient(key ssh.Signer) (*ssh.Client, error) {
	c, err := ssh.Dial("tcp", h.Server.Addr().String(), &ssh.ClientConfig{
		User:            "termchat",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(h.Server.HostKey()),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.clients = append(h.clients, c)
	h.mu.Unlock()
	return c, nil
}

// TryDial opens the SSH connection and the termchat channel.
func (h *Harness) TryDial(key ssh.Signer) (*proto.Codec, error) {
	c, err := h.SSHClient(key)
	if err != nil {
		return nil, err
	}
	ch, reqs, err := c.OpenChannel("termchat", nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return proto.NewCodec(ch), nil
}

// Dial is TryDial that fails the test on error. It sends no hello.
func (h *Harness) Dial(t *testing.T, key ssh.Signer) *proto.Codec {
	t.Helper()
	c, err := h.TryDial(key)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

var nextID atomic.Uint64

// Request sends one frame with a fresh ID and waits up to five seconds for the
// next frame from the server, which the caller can expect to be the response.
func Request(t *testing.T, c *proto.Codec, typ string, payload any) proto.Frame {
	t.Helper()
	Send(t, c, typ, payload)
	return Read(t, c)
}

// Send writes one request frame with a fresh ID and returns the ID.
func Send(t *testing.T, c *proto.Codec, typ string, payload any) uint64 {
	t.Helper()
	f := proto.Frame{ID: nextID.Add(1), Type: typ}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		f.Payload = b
	}
	if err := c.Write(f); err != nil {
		t.Fatalf("write %s: %v", typ, err)
	}
	return f.ID
}

// Read waits up to five seconds for the next frame.
func Read(t *testing.T, c *proto.Codec) proto.Frame {
	t.Helper()
	f, err := ReadErr(c, 5*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return f
}

// ReadErr reads the next frame, or returns an error after timeout. A timed-out
// read stays pending until the connection closes.
func ReadErr(c *proto.Codec, timeout time.Duration) (proto.Frame, error) {
	type result struct {
		f   proto.Frame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := c.Read()
		ch <- result{f, err}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-time.After(timeout):
		return proto.Frame{}, context.DeadlineExceeded
	}
}

// Hello sends hello with the current protocol version and returns the result.
func Hello(t *testing.T, c *proto.Codec) proto.HelloRes {
	t.Helper()
	f := Request(t, c, proto.TypeHello, proto.HelloReq{Version: proto.Version})
	if f.Error != nil {
		t.Fatalf("hello: %v", f.Error)
	}
	var res proto.HelloRes
	if err := json.Unmarshal(f.Payload, &res); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	return res
}
