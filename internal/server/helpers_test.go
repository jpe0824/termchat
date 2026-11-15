package server_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

// device is one dialled, hello'd connection. Frames with ID 0 that arrive
// while it waits for a response are kept in pushes, in arrival order.
type device struct {
	t      *testing.T
	c      *proto.Codec
	handle string
	pushes []proto.Frame
}

type chat struct {
	t    *testing.T
	h    *testutil.Harness
	keys map[string]ssh.Signer
}

func newChat(t *testing.T, handles ...string) *chat {
	t.Helper()
	return newChatWith(t, nil, handles...)
}

func newChatWith(t *testing.T, adjust func(*server.Options), handles ...string) *chat {
	t.Helper()
	ch := &chat{t: t, h: testutil.StartServerWith(t, adjust), keys: map[string]ssh.Signer{}}
	for _, name := range handles {
		ch.keys[name] = ch.h.NewPerson(t, name)
	}
	return ch
}

// dial opens another device for handle.
func (ch *chat) dial(handle string) *device {
	ch.t.Helper()
	c := ch.h.Dial(ch.t, ch.keys[handle])
	testutil.Hello(ch.t, c)
	return &device{t: ch.t, c: c, handle: handle}
}

func (d *device) send(typ string, payload any) uint64 {
	d.t.Helper()
	return testutil.Send(d.t, d.c, typ, payload)
}

// await reads frames until the response to id, stashing pushes.
func (d *device) await(id uint64) proto.Frame {
	d.t.Helper()
	for {
		f := testutil.Read(d.t, d.c)
		if f.ID == 0 {
			d.pushes = append(d.pushes, f)
			continue
		}
		if f.ID != id {
			d.t.Fatalf("response id %d, want %d (%s)", f.ID, id, f.Type)
		}
		return f
	}
}

func (d *device) req(typ string, payload any) proto.Frame {
	d.t.Helper()
	return d.await(d.send(typ, payload))
}

// do sends a request that must succeed and decodes the response into out.
func (d *device) do(typ string, payload, out any) {
	d.t.Helper()
	f := d.req(typ, payload)
	if f.Error != nil {
		d.t.Fatalf("%s as %s: %v", typ, d.handle, f.Error)
	}
	if out != nil {
		if err := json.Unmarshal(f.Payload, out); err != nil {
			d.t.Fatalf("decode %s: %v", typ, err)
		}
	}
}

// refuse sends a request that must fail with exactly code.
func (d *device) refuse(typ string, payload any, code string) {
	d.t.Helper()
	f := d.req(typ, payload)
	if f.Error == nil || f.Error.Code != code {
		d.t.Fatalf("%s as %s = %+v, want error %s", typ, d.handle, f, code)
	}
}

// push returns the next push, which must have the given type.
func (d *device) push(typ string) proto.Frame {
	d.t.Helper()
	var f proto.Frame
	if len(d.pushes) > 0 {
		f, d.pushes = d.pushes[0], d.pushes[1:]
	} else {
		f = testutil.Read(d.t, d.c)
	}
	if f.ID != 0 || f.Type != typ || f.Error != nil {
		d.t.Fatalf("%s got frame %+v, want push %s", d.handle, f, typ)
	}
	return f
}

func (d *device) eventPush() model.Event {
	d.t.Helper()
	var p proto.EventPush
	if err := json.Unmarshal(d.push(proto.TypeEvent).Payload, &p); err != nil {
		d.t.Fatal(err)
	}
	return p.Event
}

func (d *device) convPush() model.Conversation {
	d.t.Helper()
	var p proto.ConvUpdatedPush
	if err := json.Unmarshal(d.push(proto.TypeConvUpdated).Payload, &p); err != nil {
		d.t.Fatal(err)
	}
	return p.Conv
}

// quiet asserts nothing was pushed to d: a later request on the same
// connection is answered before any other frame.
func (d *device) quiet() {
	d.t.Helper()
	d.do(proto.TypePeopleList, nil, nil)
	if len(d.pushes) != 0 {
		d.t.Fatalf("%s received unexpected pushes %+v", d.handle, d.pushes)
	}
}

// closed asserts the server closed the connection.
func (d *device) closed() { d.t.Helper(); d.closedWith(io.EOF) }

// closedWith is closed for a connection that may have been cut mid-frame, where
// the codec reports io.ErrUnexpectedEOF.
func (d *device) closedWith(accept ...error) {
	d.t.Helper()
	for {
		f, err := testutil.ReadErr(d.c, 5*time.Second)
		if err != nil {
			for _, a := range accept {
				if errors.Is(err, a) {
					return
				}
			}
			d.t.Fatalf("%s: read = %v, want io.EOF", d.handle, err)
		}
		if f.ID != 0 {
			d.t.Fatalf("%s: unexpected response %+v on a connection that should close", d.handle, f)
		}
	}
}

var msgCounter atomic.Int64

func newMsgID() string { return fmt.Sprintf("m-%d", msgCounter.Add(1)) }

func text(s string) model.Body { return model.Body{Format: "text/plain", Data: []byte(s)} }

// say posts a message and returns the event.
func (d *device) say(conv, s string) model.Event {
	d.t.Helper()
	var res proto.MsgSendRes
	d.do(proto.TypeMsgSend, proto.MsgSendReq{Conv: conv, MsgID: newMsgID(), Body: text(s)}, &res)
	return res.Event
}

func (d *device) room(name string, open bool) model.Conversation {
	d.t.Helper()
	var res proto.RoomCreateRes
	d.do(proto.TypeRoomCreate, proto.RoomCreateReq{Name: name, Open: open}, &res)
	return res.Conv
}

func (d *device) invite(conv, handle string) {
	d.t.Helper()
	d.do(proto.TypeRoomInvite, proto.RoomInviteReq{Conv: conv, Handle: handle}, nil)
}

func (d *device) history(conv string) []model.Event {
	d.t.Helper()
	var res proto.ConvHistoryRes
	d.do(proto.TypeConvHistory, proto.ConvHistoryReq{Conv: conv}, &res)
	return res.Events
}

func (d *device) sync(cursors map[string]int64) proto.ConvSyncRes {
	d.t.Helper()
	var res proto.ConvSyncRes
	d.do(proto.TypeConvSync, proto.ConvSyncReq{Cursors: cursors}, &res)
	return res
}

func seqs(evs []model.Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

func unmarshal(t *testing.T, b []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}
