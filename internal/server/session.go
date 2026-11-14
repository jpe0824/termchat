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
// session's own goroutine; other goroutines must go through Server.Sessions.
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
	sess.ctx, sess.cancel = context.WithCancel(context.Background())
	return sess, nil
}

func (s *Session) identity() (model.Person, bool) {
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

// Push writes an unsolicited frame with ID 0. It is safe from any goroutine.
func (s *Session) Push(typ string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.write(proto.Frame{Type: typ, Payload: b})
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
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		s.watchKey()
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
	if _, known := s.identity(); !known {
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
		return s.reply(f, nil, &proto.Error{Code: proto.CodeRateLimited, Message: "too many requests"})
	}
	if s.Known && !s.keyPresent() {
		return false
	}
	if !s.helloed && f.Type != proto.TypeHello {
		return s.reply(f, nil, &proto.Error{Code: proto.CodeInvalid, Message: "first frame must be hello"})
	}
	if !s.Known && f.Type != proto.TypeHello && f.Type != proto.TypeInviteRedeem {
		return s.reply(f, nil, &proto.Error{Code: proto.CodeNotAuthorised, Message: "unknown key"})
	}
	h := s.srv.handler(f.Type)
	if h == nil {
		return s.reply(f, nil, &proto.Error{Code: proto.CodeInvalid, Message: "unknown request type"})
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
	var p struct {
		Body struct {
			Data []byte `json:"data"`
		} `json:"body"`
	}
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return &proto.Error{Code: proto.CodeInvalid, Message: "malformed payload"}
	}
	if len(p.Body.Data) > s.srv.limits.MaxBody {
		return &proto.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("body exceeds %d bytes", s.srv.limits.MaxBody)}
	}
	return nil
}

func (s *Session) dispatch(h HandlerFunc, f proto.Frame) (res any, perr *proto.Error) {
	defer func() {
		if r := recover(); r != nil {
			s.srv.log.Error("handler panic", "type", f.Type, "id", f.ID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			res, perr = nil, &proto.Error{Code: proto.CodeInvalid, Message: "internal error"}
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
			out.Error = &proto.Error{Code: proto.CodeInvalid, Message: "internal error"}
		} else {
			out.Payload = b
		}
	}
	err := s.write(out)
	if errors.Is(err, proto.ErrFrameTooLarge) {
		out = proto.Frame{ID: f.ID, Type: f.Type, Error: &proto.Error{Code: proto.CodeTooLarge, Message: "response too large"}}
		err = s.write(out)
	}
	return err == nil
}

func (s *Server) handleHello(_ context.Context, sess *Session, f proto.Frame) (any, *proto.Error) {
	var req proto.HelloReq
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return nil, &proto.Error{Code: proto.CodeInvalid, Message: "malformed payload"}
	}
	if req.Version != proto.Version {
		return nil, &proto.Error{Code: proto.CodeVersionMismatch, Message: fmt.Sprintf("server speaks version %d", proto.Version)}
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
	var req proto.InviteRedeemReq
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return nil, &proto.Error{Code: proto.CodeInvalid, Message: "malformed payload"}
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
		return nil, &proto.Error{Code: proto.CodeNotAuthorised, Message: "invite not valid"}
	default:
		s.log.Error("redeem failed", "err", err)
		return nil, &proto.Error{Code: proto.CodeInvalid, Message: "internal error"}
	}
}
