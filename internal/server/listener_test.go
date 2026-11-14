package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

func fingerprint(s ssh.Signer) string { return ssh.FingerprintSHA256(s.PublicKey()) }

func redeem(t *testing.T, c *proto.Codec, code string) proto.Frame {
	t.Helper()
	return testutil.Request(t, c, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: code})
}

func TestHostKeyStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	open := func() ssh.PublicKey {
		s, err := server.New(server.Options{Addr: "127.0.0.1:0", DataDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		return s.HostKey()
	}
	first, second := open(), open()
	if !bytes.Equal(first.Marshal(), second.Marshal()) {
		t.Fatal("host key changed across restart")
	}
	if first.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("host key type %q", first.Type())
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "host_key"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("host_key mode %v", fi.Mode().Perm())
		}
	}
}

func TestKnownKeyHelloReturnsPerson(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	res := testutil.Hello(t, c)
	if res.Version != proto.Version || res.Person == nil || res.Person.Handle != "alice" {
		t.Fatalf("hello = %+v", res)
	}
}

func TestUnknownKeyCanOnlyHelloAndRedeem(t *testing.T) {
	h := testutil.StartServer(t)
	key := testutil.NewKey(t)
	c := h.Dial(t, key)
	if res := testutil.Hello(t, c); res.Person != nil {
		t.Fatalf("unknown key got person %+v", res.Person)
	}
	f := testutil.Request(t, c, proto.TypeConvList, nil)
	if f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
		t.Fatalf("conv.list = %+v", f)
	}

	code, err := h.Store.CreateInvite("bob", "Bob", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f = redeem(t, c, code)
	if f.Error != nil {
		t.Fatalf("redeem: %v", f.Error)
	}
	var res proto.InviteRedeemRes
	if err := json.Unmarshal(f.Payload, &res); err != nil || res.Person.Handle != "bob" {
		t.Fatalf("redeem res %s %v", f.Payload, err)
	}
	if got := testutil.Hello(t, c); got.Person == nil || got.Person.Handle != "bob" {
		t.Fatalf("hello after redeem = %+v", got)
	}
	if ok, _ := h.Store.HasKey(fingerprint(key)); !ok {
		t.Fatal("key not stored")
	}
	if n := len(h.Server.Sessions("bob")); n != 1 {
		t.Fatalf("Sessions(bob) = %d", n)
	}
}

func TestRedeemFailuresAreNotAuthorised(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, c)
	if f := redeem(t, c, "NOSUCHCODE"); f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
		t.Fatalf("bad code = %+v", f)
	}

	// A key already registered cannot redeem a second invite.
	known := h.NewPerson(t, "alice")
	code, _ := h.Store.CreateInvite("carol", "Carol", time.Hour)
	c2 := h.Dial(t, known)
	testutil.Hello(t, c2)
	if f := redeem(t, c2, code); f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
		t.Fatalf("known key redeem = %+v", f)
	}
}

func TestVersionMismatchRejected(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	id := testutil.Send(t, c, proto.TypeHello, proto.HelloReq{Version: 99})
	f := testutil.Read(t, c)
	if f.Error == nil || f.Error.Code != proto.CodeVersionMismatch || f.ID != id || f.Type != proto.TypeHello {
		t.Fatalf("got %+v (want id %d)", f, id)
	}
}

func TestFirstFrameMustBeHello(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	id := testutil.Send(t, c, proto.TypeConvList, nil)
	f := testutil.Read(t, c)
	if f.Error == nil || f.Error.Code != proto.CodeInvalid || f.ID != id || f.Type != proto.TypeConvList {
		t.Fatalf("got %+v", f)
	}
	if got := testutil.Hello(t, c); got.Person == nil {
		t.Fatal("hello after rejected first frame should still work")
	}
}

func TestUnknownTypeInvalid(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	testutil.Hello(t, c)
	id := testutil.Send(t, c, "no.such.type", nil)
	f := testutil.Read(t, c)
	if f.Error == nil || f.Error.Code != proto.CodeInvalid || f.ID != id || f.Type != "no.such.type" {
		t.Fatalf("got %+v", f)
	}
}

func TestHandlerPanicRecovered(t *testing.T) {
	h := testutil.StartServer(t)
	h.Server.Handle(proto.TypePeopleList, func(context.Context, *server.Session, proto.Frame) (any, *proto.Error) {
		panic("boom")
	})
	c := h.Dial(t, h.NewPerson(t, "alice"))
	testutil.Hello(t, c)
	f := testutil.Request(t, c, proto.TypePeopleList, nil)
	if f.Error == nil || f.Error.Code != proto.CodeInvalid {
		t.Fatalf("got %+v", f)
	}
	if got := testutil.Hello(t, c); got.Person == nil {
		t.Fatal("connection unusable after panic")
	}
}

func TestRateLimit(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	const n = 100
	for i := 0; i < n; i++ {
		testutil.Send(t, c, proto.TypeHello, proto.HelloReq{Version: proto.Version})
	}
	var limited, ok int
	for i := 0; i < n; i++ {
		f := testutil.Read(t, c)
		switch {
		case f.Error == nil:
			ok++
		case f.Error.Code == proto.CodeRateLimited:
			limited++
			if f.Type != proto.TypeHello || f.ID == 0 {
				t.Fatalf("rate_limited response lost id/type: %+v", f)
			}
		default:
			t.Fatalf("unexpected %+v", f)
		}
	}
	if limited == 0 || ok == 0 {
		t.Fatalf("ok=%d limited=%d", ok, limited)
	}
	time.Sleep(300 * time.Millisecond)
	if got := testutil.Hello(t, c); got.Person == nil {
		t.Fatal("connection did not recover")
	}
}

func TestOversizeBodyRejected(t *testing.T) {
	h := testutil.StartServer(t)
	var dispatched atomic.Int32
	stub := func(context.Context, *server.Session, proto.Frame) (any, *proto.Error) {
		dispatched.Add(1)
		return struct{}{}, nil
	}
	h.Server.Handle(proto.TypeMsgSend, stub)
	h.Server.Handle(proto.TypeMsgEdit, stub)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	testutil.Hello(t, c)

	send := func(typ string, size int) proto.Frame {
		body := model.Body{Format: "text", Data: bytes.Repeat([]byte("x"), size)}
		return testutil.Request(t, c, typ, proto.MsgSendReq{Conv: "c", MsgID: "m", Body: body})
	}
	for _, typ := range []string{proto.TypeMsgSend, proto.TypeMsgEdit} {
		if f := send(typ, 8193); f.Error == nil || f.Error.Code != proto.CodeTooLarge {
			t.Fatalf("%s 8193: %+v", typ, f)
		}
	}
	if dispatched.Load() != 0 {
		t.Fatal("oversize frame reached the handler")
	}
	if f := send(proto.TypeMsgSend, 8192); f.Error != nil {
		t.Fatalf("8192: %+v", f)
	}
	if dispatched.Load() != 1 {
		t.Fatal("exact-limit frame not dispatched")
	}
}

func TestAuthFailureCap(t *testing.T) {
	h := testutil.StartServer(t)
	now := time.Now()
	var clock atomic.Int64 // seconds offset
	server.SetClock(h.Server, func() time.Time { return now.Add(time.Duration(clock.Load()) * time.Second) })

	c := h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, c)
	for i := 0; i < 6; i++ {
		if f := redeem(t, c, "BADCODE"); f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
			t.Fatalf("attempt %d: %+v", i, f)
		}
	}
	if _, err := testutil.ReadErr(c, 2*time.Second); err == nil {
		t.Fatal("connection still open after exceeding the cap")
	}
	if _, err := h.TryDial(testutil.NewKey(t)); err == nil {
		t.Fatal("new connection accepted from a blocked address")
	}

	clock.Store(61)
	c2, err := h.TryDial(testutil.NewKey(t))
	if err != nil {
		t.Fatalf("dial after window cleared: %v", err)
	}
	testutil.Hello(t, c2)
}

func TestFiveFailuresStillAllowed(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, c)
	for i := 0; i < 5; i++ {
		redeem(t, c, "BADCODE")
	}
	if got := testutil.Hello(t, c); got.Version != proto.Version {
		t.Fatal("connection closed at exactly the cap")
	}
}

func TestRemovedKeyDisconnected(t *testing.T) {
	h := testutil.StartServer(t)
	server.SetRecheckInterval(h.Server, 50*time.Millisecond)
	key := h.NewPerson(t, "alice")
	c := h.Dial(t, key)
	testutil.Hello(t, c)
	if err := h.Store.RemoveKey(fingerprint(key)); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ReadErr(c, time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("read after RemoveKey: %v", err)
	}
}

func TestRemovedKeyCaughtOnNextFrame(t *testing.T) {
	h := testutil.StartServer(t) // default 2s ticker: only the per-frame check can win
	key := h.NewPerson(t, "alice")
	c := h.Dial(t, key)
	testutil.Hello(t, c)
	if err := h.Store.RemoveKey(fingerprint(key)); err != nil {
		t.Fatal(err)
	}
	testutil.Send(t, c, proto.TypeHello, proto.HelloReq{Version: proto.Version})
	if f, err := testutil.ReadErr(c, time.Second); err == nil {
		t.Fatalf("got a response on a removed key: %+v", f)
	}
}

func TestKickClosesSessions(t *testing.T) {
	h := testutil.StartServer(t)
	key := h.NewPerson(t, "alice")
	c := h.Dial(t, key)
	testutil.Hello(t, c)
	h.Server.Kick(fingerprint(key))
	if _, err := testutil.ReadErr(c, time.Second); err == nil {
		t.Fatal("session survived Kick")
	}
}

func TestNonTermchatChannelRejected(t *testing.T) {
	h := testutil.StartServer(t)
	cl, err := h.SSHClient(h.NewPerson(t, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"session", "direct-tcpip"} {
		if _, _, err := cl.OpenChannel(typ, nil); err == nil {
			t.Fatalf("channel %q accepted", typ)
		}
	}
	ch, reqs, err := cl.OpenChannel("termchat", nil)
	if err != nil {
		t.Fatalf("termchat channel: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, err := ch.SendRequest("shell", true, nil); err == nil && ok {
		t.Fatal("shell request accepted")
	}
}

func TestPushAndSessions(t *testing.T) {
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	testutil.Hello(t, c)
	ss := h.Server.Sessions("alice")
	if len(ss) != 1 || !ss[0].Known || ss[0].Person.Handle != "alice" {
		t.Fatalf("Sessions = %+v", ss)
	}
	if n := len(h.Server.Sessions("nobody")); n != 0 {
		t.Fatalf("Sessions(nobody) = %d", n)
	}
	if err := ss[0].Push(proto.TypeConvUpdated, proto.ConvUpdatedPush{Conv: model.Conversation{ID: "c1"}}); err != nil {
		t.Fatal(err)
	}
	f := testutil.Read(t, c)
	var p proto.ConvUpdatedPush
	if f.ID != 0 || f.Type != proto.TypeConvUpdated || json.Unmarshal(f.Payload, &p) != nil || p.Conv.ID != "c1" {
		t.Fatalf("push = %+v", f)
	}
}

func TestServeReturnsOnCancelAndClosesSessions(t *testing.T) {
	before := runtime.NumGoroutine()
	h := testutil.StartServer(t)
	c := h.Dial(t, h.NewPerson(t, "alice"))
	testutil.Hello(t, c)
	if err := h.Stop(); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	if _, err := testutil.ReadErr(c, time.Second); err == nil {
		t.Fatal("session open after Serve returned")
	}
	if len(h.Server.Sessions("alice")) != 0 {
		t.Fatal("sessions remain after Serve returned")
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d -> %d\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

func TestSilentConnectionTimesOut(t *testing.T) {
	h := testutil.StartServer(t)
	server.SetHandshakeTimeout(h.Server, 200*time.Millisecond)
	conn, err := net.Dial("tcp", h.Server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	for {
		if _, err := conn.Read(buf); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("server held a silent connection open")
			}
			return
		}
	}
}
