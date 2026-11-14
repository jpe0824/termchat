package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

func fastLimits(o *server.Options) {
	o.Limits = server.Limits{FramesPerSec: 100000, Burst: 100000}
}

// sendMany posts n messages from d without using the test's Fatal paths, so it
// may run on its own goroutine.
func sendMany(d *device, conv string, n int, body model.Body, errs chan<- error) {
	for i := 0; i < n; i++ {
		payload, _ := json.Marshal(proto.MsgSendReq{Conv: conv, MsgID: newMsgID(), Body: body})
		id := uint64(i + 1)
		if err := d.c.Write(proto.Frame{ID: id, Type: proto.TypeMsgSend, Payload: payload}); err != nil {
			errs <- err
			return
		}
		for {
			f, err := testutil.ReadErr(d.c, 20*time.Second)
			if err != nil {
				errs <- fmt.Errorf("%s message %d: %w", d.handle, i, err)
				return
			}
			if f.ID == 0 {
				continue
			}
			if f.ID != id || f.Error != nil {
				errs <- fmt.Errorf("%s message %d: response %+v", d.handle, i, f)
				return
			}
			break
		}
	}
	errs <- nil
}

// collectEvents reads n event pushes from d, ignoring other frames.
func collectEvents(d *device, n int) ([]model.Event, error) {
	var out []model.Event
	for len(out) < n {
		f, err := testutil.ReadErr(d.c, 20*time.Second)
		if err != nil {
			return out, fmt.Errorf("%s after %d events: %w", d.handle, len(out), err)
		}
		if f.Type != proto.TypeEvent {
			continue
		}
		var p proto.EventPush
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return out, err
		}
		out = append(out, p.Event)
	}
	return out, nil
}

func TestConcurrentSendsArriveInSeqOrder(t *testing.T) {
	names := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "s10", "s11", "s12"}
	ch := newChatWith(t, fastLimits, append([]string{"alice"}, names...)...)
	a := ch.dial("alice")
	senders := make([]*device, len(names))
	for i, n := range names {
		senders[i] = ch.dial(n)
	}
	conv := roomWith(a, "general", senders...)
	const each = 50
	total := each * len(senders)

	errs := make(chan error, len(senders)+1)
	var got []model.Event
	go func() {
		var err error
		got, err = collectEvents(a, total)
		errs <- err
	}()
	body := text("race")
	for _, d := range senders {
		go sendMany(d, conv.ID, each, body, errs)
	}
	for i := 0; i < len(senders)+1; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	first := int64(len(senders) + 2) // room creation plus one join per sender
	for i, ev := range got {
		if want := first + int64(i); ev.Seq != want {
			t.Fatalf("push %d has seq %d, want %d", i, ev.Seq, want)
		}
	}
}

func TestStalledRecipientDoesNotDelaySender(t *testing.T) {
	ch := newChatWith(t, fastLimits, "alice", "bob", "carol")
	a, b, c := ch.dial("alice"), ch.dial("bob"), ch.dial("carol")
	conv := roomWith(a, "general", b, c)
	const n = 500
	body := model.Body{Format: "text/plain", Data: []byte(strings.Repeat("s", 8192))}

	// bob never reads again; carol keeps up.
	errs := make(chan error, 2)
	var got []model.Event
	go func() {
		var err error
		got, err = collectEvents(c, n)
		errs <- err
	}()
	start := time.Now()
	go sendMany(a, conv.ID, n, body, errs)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	// The write watchdog alone takes 10s to give up on a stalled client.
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("sending to a room with a stalled member took %v", d)
	}
	for i, ev := range got {
		if want := int64(4 + i); ev.Seq != want {
			t.Fatalf("carol push %d has seq %d, want %d", i, ev.Seq, want)
		}
	}
	b.closedWith(io.EOF, io.ErrUnexpectedEOF)
}
