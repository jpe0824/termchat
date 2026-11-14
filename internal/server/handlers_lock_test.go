package server

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// waitBlockedInLockConv returns once some goroutine is parked on a mutex
// inside lockConv.
func waitBlockedInLockConv(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stacks := string(buf[:runtime.Stack(buf, true)])
		for _, g := range strings.Split(stacks, "\n\n") {
			if strings.Contains(g, "sync.(*Mutex).Lock") && strings.Contains(g, ".lockConv(") {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("no request is waiting on the conversation lock")
}

// A person removed from a room while their request waits on the room's lock
// is refused as a non-member would be, and their message does not land.
func TestRemovedWhileWaitingOnLockIsRefused(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	st := srv.store
	people(t, st, "alice", "bob")
	bob, err := st.Person("bob")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		open bool
		code string
	}{
		{open: true, code: proto.CodeForbidden},
		{open: false, code: proto.CodeNotFound},
	} {
		name := "private"
		if tc.open {
			name = "open"
		}
		t.Run(name, func(t *testing.T) {
			conv, err := st.CreateRoom(name, tc.open, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddMember(conv.ID, "alice", "bob"); err != nil {
				t.Fatal(err)
			}

			unlock := srv.hub.lock(conv.ID)
			done := make(chan error, 1)
			go func() {
				_, err := srv.msgSend(context.Background(), call{me: bob}, proto.MsgSendReq{
					Conv: conv.ID, MsgID: "m1", Body: model.Body{Format: "text", Data: []byte("late")},
				})
				done <- err
			}()
			waitBlockedInLockConv(t)
			if _, err := st.RemoveMember(conv.ID, "alice", "bob"); err != nil {
				unlock()
				t.Fatal(err)
			}
			unlock()

			var pe *proto.Error
			if err := <-done; !errors.As(err, &pe) || pe.Code != tc.code {
				t.Fatalf("send after removal = %v, want %s", err, tc.code)
			}
			evs, err := st.After(conv.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if last := evs[len(evs)-1]; last.Kind != model.MemberLeft {
				t.Fatalf("last event = %+v, want member.left", last)
			}
		})
	}
}

// Requests for conversation ids that do not exist all answer the same
// not_found and never add to the lock map.
func TestMadeUpConversationIDsTakeNoLock(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	people(t, srv.store, "bob")
	bob, err := srv.store.Person("bob")
	if err != nil {
		t.Fatal(err)
	}
	before := LockCount(srv)
	for i := range 100 {
		_, err := srv.msgSend(context.Background(), call{me: bob}, proto.MsgSendReq{
			Conv: fmt.Sprintf("r_madeup%03d", i), MsgID: "m1", Body: model.Body{Format: "text", Data: []byte("x")},
		})
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != proto.CodeNotFound || pe.Message != "not found" {
			t.Fatalf("send to made-up id %d = %v, want not_found \"not found\"", i, err)
		}
	}
	if after := LockCount(srv); after != before {
		t.Fatalf("lock map grew from %d to %d", before, after)
	}
}
