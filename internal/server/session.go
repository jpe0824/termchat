package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/time/rate"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// Session is one client connection. Person and Known are written only by the
// session's own goroutine; other goroutines must use Identity.
type Session struct {
	Person      model.Person
	Fingerprint string
	Known       bool

	srv     *Server
	pub     []byte
	ip      string
	sconn   *ssh.ServerConn
	codec   *proto.Codec
	limiter *rate.Limiter
	recheck time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	// out carries every frame written after the loop starts, so pushes and
	// responses reach the client in the order they were queued.
	out chan outFrame

	mu        sync.Mutex // guards Person and Known against readers on other goroutines
	closeOnce sync.Once
	helloed   bool // a successful hello has been seen
	closing   bool // close once the current response is written
}

func (s *Server) newSession(conn net.Conn, sconn *ssh.ServerConn) (*Session, error) {
	pub, err := base64.StdEncoding.DecodeString(sconn.Permissions.Extensions["key"])
	if err != nil {
		return nil, err
	}
	sess := &Session{
		Fingerprint: sconn.Permissions.Extensions["fingerprint"],
		srv:         s,
		pub:         pub,
		ip:          remoteIP(conn.RemoteAddr()),
		sconn:       sconn,
		limiter:     rate.NewLimiter(rate.Limit(s.limits.FramesPerSec), s.limits.Burst),
	}
	s.mu.Lock()
	sess.recheck = s.recheck
	s.mu.Unlock()
	person, err := s.store.PersonByKey(sess.Fingerprint)
	switch {
	case err == nil:
		sess.Person, sess.Known = person, true
	case errors.Is(err, ErrNotFound):
	default:
		return nil, fmt.Errorf("lookup key: %w", err)
	}
	sess.out = make(chan outFrame, outboxSize)
	sess.ctx, sess.cancel = context.WithCancel(context.Background())
	return sess, nil
}

// Identity returns the session's person and whether the key is known. It is
// safe from any goroutine; only it, not the Person and Known fields, may be
// used outside the session's own goroutine.
func (s *Session) Identity() (model.Person, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Person, s.Known
}

func (s *Session) promote(p model.Person) {
	s.mu.Lock()
	s.Person, s.Known = p, true
	s.mu.Unlock()
}

// Close ends the session immediately. It is safe from any goroutine.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.sconn.Close()
	})
}

// outboxSize is how many queued pushes a session may fall behind by before the
// server drops it; the client reconnects and catches up with conv.sync.
const outboxSize = 256

// outFrame is a queued frame. done is non-nil for responses, whose writer
// needs the result.
type outFrame struct {
	f    proto.Frame
	done chan error
}

// enqueue queues an unsolicited frame without blocking. A session that has
// fallen outboxSize frames behind is closed rather than skipped, so a client
// never silently misses a push.
func (s *Session) enqueue(typ string, payload []byte) {
	select {
	case s.out <- outFrame{f: proto.Frame{Type: typ, Payload: payload}}:
	default:
		s.srv.log.Warn("dropping session that fell behind", "fingerprint", s.Fingerprint)
		s.Close()
	}
}

// drain writes queued frames in order until the session ends or a write fails.
func (s *Session) drain() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case o := <-s.out:
			err := s.write(o.f)
			if o.done != nil {
				o.done <- err
			}
			switch {
			case err == nil:
			case errors.Is(err, proto.ErrFrameTooLarge):
				// Only this frame is lost, and the caller of a response
				// is told; the connection is still usable.
				if o.done == nil {
					s.srv.log.Error("push exceeds frame limit", "type", o.f.Type)
				}
			default:
				s.Close()
				return
			}
		}
	}
}

// write bounds how long a client that stops reading can hold a writer.
func (s *Session) write(f proto.Frame) error {
	t := time.AfterFunc(writeTimeout, s.Close)
	defer t.Stop()
	return s.codec.Write(f)
}

func (s *Session) run(ch ssh.Channel) {
	s.codec = proto.NewCodec(ch)
	s.srv.mu.Lock()
	s.srv.sessions[s] = struct{}{}
	s.srv.mu.Unlock()

	var watcher sync.WaitGroup
	watcher.Add(2)
	go func() {
		defer watcher.Done()
		s.watchKey()
	}()
	go func() {
		defer watcher.Done()
		s.drain()
	}()
	defer func() {
		s.Close()
		watcher.Wait()
		s.srv.mu.Lock()
		delete(s.srv.sessions, s)
		s.srv.mu.Unlock()
	}()

	for {
		f, err := s.codec.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.srv.log.Debug("read ended", "remote", s.ip, "err", err)
			}
			return
		}
		if !s.handleFrame(f) {
			return
		}
	}
}

// watchKey closes the session when its key leaves the store; the admin CLI
// removes keys from another process and cannot call Kick.
func (s *Session) watchKey() {
	t := time.NewTicker(s.recheck)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			if !s.keyPresent() {
				s.Close()
				return
			}
		}
	}
}

// keyPresent is true for unknown sessions and when the store cannot answer,
// so a store error does not disconnect everyone.
func (s *Session) keyPresent() bool {
	if _, known := s.Identity(); !known {
		return true
	}
	ok, err := s.srv.store.HasKey(s.Fingerprint)
	if err != nil {
		s.srv.log.Error("key recheck failed", "err", err)
		return true
	}
	return ok
}

// handleFrame answers one request and reports whether to keep reading.
func (s *Session) handleFrame(f proto.Frame) bool {
	if !s.limiter.Allow() {
		return s.reply(f, nil, protoErr(proto.CodeRateLimited, "too many requests"))
	}
	if s.Known && !s.keyPresent() {
		return false
	}
	if !s.helloed && f.Type != proto.TypeHello {
		return s.reply(f, nil, protoErr(proto.CodeInvalid, "first frame must be hello"))
	}
	if !s.Known && f.Type != proto.TypeHello && f.Type != proto.TypeInviteRedeem {
		return s.reply(f, nil, protoErr(proto.CodeNotAuthorised, "unknown key"))
	}
	h := s.srv.handler(f.Type)
	if h == nil {
		return s.reply(f, nil, protoErr(proto.CodeInvalid, "unknown request type"))
	}
	if f.Type == proto.TypeMsgSend || f.Type == proto.TypeMsgEdit {
		if perr := s.checkBodySize(f); perr != nil {
			return s.reply(f, nil, perr)
		}
	}
	res, perr := s.dispatch(h, f)
	if !s.reply(f, res, perr) {
		return false
	}
	return !s.closing
}

// checkBodySize decodes only Body.Data, to measure it.
func (s *Session) checkBodySize(f proto.Frame) *proto.Error {
	type bodyOnly struct {
		Body struct {
			Data []byte `json:"data"`
		} `json:"body"`
	}
	p, perr := decode[bodyOnly](f.Payload, false)
	if perr != nil {
		return perr
	}
	if len(p.Body.Data) > s.srv.limits.MaxBody {
		return protoErr(proto.CodeTooLarge, fmt.Sprintf("body exceeds %d bytes", s.srv.limits.MaxBody))
	}
	return nil
}

func (s *Session) dispatch(h HandlerFunc, f proto.Frame) (res any, perr *proto.Error) {
	defer func() {
		if r := recover(); r != nil {
			s.srv.log.Error("handler panic", "type", f.Type, "id", f.ID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			res, perr = nil, internalError()
		}
	}()
	return h(s.ctx, s, f)
}

// reply writes the response to f and reports whether the write succeeded.
func (s *Session) reply(f proto.Frame, res any, perr *proto.Error) bool {
	out := proto.Frame{ID: f.ID, Type: f.Type}
	if perr != nil {
		out.Error = perr
	} else {
		if res == nil {
			res = struct{}{}
		}
		b, err := json.Marshal(res)
		if err != nil {
			s.srv.log.Error("encode response", "type", f.Type, "err", err)
			out.Error = internalError()
		} else {
			out.Payload = b
		}
	}
	err := s.queue(out)
	if errors.Is(err, proto.ErrFrameTooLarge) {
		out = proto.Frame{ID: f.ID, Type: f.Type, Error: protoErr(proto.CodeTooLarge, "response too large")}
		err = s.queue(out)
	}
	return err == nil
}

// queue sends a response behind everything already queued and waits for the
// write, so a push queued before a response is on the wire before it.
func (s *Session) queue(f proto.Frame) error {
	o := outFrame{f: f, done: make(chan error, 1)}
	select {
	case s.out <- o:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	select {
	case err := <-o.done:
		return err
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *Server) handleHello(_ context.Context, sess *Session, f proto.Frame) (any, *proto.Error) {
	req, perr := decode[proto.HelloReq](f.Payload, false)
	if perr != nil {
		return nil, perr
	}
	if req.Version != proto.Version {
		return nil, protoErr(proto.CodeVersionMismatch, fmt.Sprintf("server speaks version %d", proto.Version))
	}
	sess.helloed = true
	res := proto.HelloRes{Version: proto.Version}
	if sess.Known {
		p := sess.Person
		res.Person = &p
	}
	return res, nil
}

func (s *Server) handleRedeem(_ context.Context, sess *Session, f proto.Frame) (any, *proto.Error) {
	req, perr := decode[proto.InviteRedeemReq](f.Payload, false)
	if perr != nil {
		return nil, perr
	}
	p, err := s.store.RedeemInvite(req.Code, sess.Fingerprint, sess.pub)
	switch {
	case err == nil:
		sess.promote(p)
		return proto.InviteRedeemRes{Person: p}, nil
	case errors.Is(err, ErrInviteInvalid), errors.Is(err, ErrExists):
		if s.recordAuthFailure(sess.ip) {
			sess.closing = true
		}
		return nil, protoErr(proto.CodeNotAuthorised, "invite not valid")
	default:
		s.log.Error("redeem failed", "err", err)
		return nil, internalError()
	}
}
