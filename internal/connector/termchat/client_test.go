package termchat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
)

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want 0600", path, fi.Mode().Perm())
	}
}

func TestJoinGeneratesKeyAndPinsHost(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	code, _ := e.h.Store.CreateDeviceInvite("ann", server.InviteTTL)
	dir := t.TempDir()

	acct, err := Join(context.Background(), e.addr, code, dir)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if acct.Handle != "ann" || acct.Name != "ann" || acct.Server != e.addr {
		t.Fatalf("account = %+v", acct)
	}

	keyFile := filepath.Join(dir, "keys", fileSafe(e.addr)+".ed25519")
	assertMode0600(t, keyFile)
	b, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatalf("key file is not in OpenSSH format: %.40q", b)
	}
	if strings.ContainsAny(filepath.Base(keyFile), ":") {
		t.Fatalf("key file name %q contains a colon", filepath.Base(keyFile))
	}

	pinFile := filepath.Join(dir, "known_servers")
	assertMode0600(t, pinFile)
	pin, err := os.ReadFile(pinFile)
	if err != nil {
		t.Fatal(err)
	}
	want := e.addr + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(e.h.Server.HostKey())))
	if strings.TrimSpace(string(pin)) != want {
		t.Fatalf("known_servers = %q, want %q", pin, want)
	}
}

func TestJoinReusesKeyAndDoesNotSpendInviteWhenRegistered(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	first, _ := e.h.Store.CreateDeviceInvite("ann", server.InviteTTL)
	dir := t.TempDir()
	if _, err := Join(context.Background(), e.addr, first, dir); err != nil {
		t.Fatal(err)
	}
	keyFile := keyPath(dir, e.addr)
	before, _ := os.ReadFile(keyFile)

	spare, _ := e.h.Store.CreateDeviceInvite("ann", server.InviteTTL)
	acct, err := Join(context.Background(), e.addr, spare, dir)
	if err != nil || acct.Handle != "ann" {
		t.Fatalf("second Join = %+v, %v", acct, err)
	}
	after, _ := os.ReadFile(keyFile)
	if string(before) != string(after) {
		t.Fatal("device key changed on the second Join")
	}
	// The spare invite is still redeemable from a different device.
	if _, err := Join(context.Background(), e.addr, spare, t.TempDir()); err != nil {
		t.Fatalf("spare invite was spent: %v", err)
	}
}

func TestJoinWithBadOrExpiredCode(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	expired, _ := e.h.Store.CreateDeviceInvite("ann", -time.Hour)
	for name, code := range map[string]string{"unknown": "NOSUCHCODE", "expired": expired} {
		_, err := Join(context.Background(), e.addr, code, t.TempDir())
		if !errors.Is(err, connector.ErrNotAuthorised) {
			t.Fatalf("%s: err = %v, want ErrNotAuthorised", name, err)
		}
		if !strings.Contains(err.Error(), "invite not valid") {
			t.Fatalf("%s: err = %q lacks the server's message", name, err)
		}
	}
}

func TestJoinRefusesDifferentPinnedHostKey(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	code, _ := e.h.Store.CreateDeviceInvite("ann", server.InviteTTL)
	dir := t.TempDir()
	if _, err := Join(context.Background(), e.addr, code, dir); err != nil {
		t.Fatal(err)
	}
	pinBefore, _ := os.ReadFile(filepath.Join(dir, "known_servers"))

	e.restart(t, t.TempDir()) // new data directory, so a new host key
	_, err := Join(context.Background(), e.addr, "ANYCODE", dir)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("err = %v, want ErrHostKeyChanged", err)
	}
	pinAfter, _ := os.ReadFile(filepath.Join(dir, "known_servers"))
	if string(pinBefore) != string(pinAfter) {
		t.Fatal("known_servers was rewritten")
	}
}

func TestHostKeyChangeRefused(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	var dials atomic.Int32
	c.dial = func(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		dials.Add(1)
		return defaultDial(ctx, addr, cfg)
	}
	c.after = (&immediate{}).after
	e.restart(t, t.TempDir())

	start(t, c)
	drain(t, c)
	waitState(t, c, connector.StateHostKeyChanged)
	n := dials.Load()
	time.Sleep(100 * time.Millisecond)
	if dials.Load() != n {
		t.Fatal("client dialled again in a terminal state")
	}
	if _, err := c.People(context.Background()); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("People err = %v, want ErrOffline", err)
	}
}

func TestReconnectAfterServerRestart(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c := e.client(t, "ann")
	drain(t, c)

	if err := e.h.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, connector.StateOffline)
	if _, err := c.People(context.Background()); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("People while down: %v, want ErrOffline", err)
	}
	e.restart(t, e.dataDir)
	waitState(t, c, connector.StateConnected)
	people, err := c.People(context.Background())
	if err != nil || len(people) != 1 || people[0].Handle != "ann" {
		t.Fatalf("People after reconnect = %+v, %v", people, err)
	}
}

func TestBackoffSchedule(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	clock := &immediate{}
	c.after = clock.after
	c.dial = func(context.Context, string, *ssh.ClientConfig) (*ssh.Client, error) {
		return nil, errors.New("down")
	}
	drain(t, c)
	stop := start(t, c)
	deadline := time.Now().Add(wait)
	for len(clock.snapshot()) < 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	s := time.Second
	want := []time.Duration{1 * s, 2 * s, 4 * s, 8 * s, 16 * s, 30 * s, 30 * s, 30 * s}
	if got := clock.snapshot(); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("delays = %v, want prefix %v", got, want)
	}
}

func TestBackoffResetsAfterSuccessfulConnect(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, dir := e.joined(t, "ann")
	clock := &immediate{}
	c.after = clock.after
	var dials atomic.Int32
	c.dial = func(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		if dials.Add(1) == 4 {
			return defaultDial(ctx, addr, cfg)
		}
		return nil, errors.New("down")
	}
	drain(t, c)
	stop := start(t, c)
	waitState(t, c, connector.StateConnected)
	e.h.Server.Kick(fingerprint(t, dir, e.addr))

	deadline := time.Now().Add(wait)
	for len(clock.snapshot()) < 6 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	s := time.Second
	// Three failed dials grow the delay; the fourth connects, and after it
	// drops the schedule starts again at one second.
	want := []time.Duration{1 * s, 2 * s, 4 * s, 1 * s, 2 * s, 4 * s}
	if got := clock.snapshot(); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("delays = %v, want prefix %v", got, want)
	}
}

func TestRemovedKeyBecomesNotAuthorised(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, dir := e.joined(t, "ann")
	var dials atomic.Int32
	clock := &immediate{}
	c.after = clock.after
	c.dial = func(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		dials.Add(1)
		return defaultDial(ctx, addr, cfg)
	}
	drain(t, c)
	start(t, c)
	waitState(t, c, connector.StateConnected)

	fp := fingerprint(t, dir, e.addr)
	if err := e.h.Store.RemoveKey(fp); err != nil {
		t.Fatal(err)
	}
	e.h.Server.Kick(fp)
	waitState(t, c, connector.StateNotAuthorised)

	n, d := dials.Load(), len(clock.snapshot())
	time.Sleep(100 * time.Millisecond)
	if dials.Load() != n || len(clock.snapshot()) != d {
		t.Fatal("client kept dialling after not_authorised")
	}
	if _, err := c.People(context.Background()); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("People err = %v, want ErrOffline", err)
	}
}

func TestVersionMismatchIsTerminal(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	e.h.Server.Handle(proto.TypeHello, func(context.Context, *server.Session, proto.Frame) (any, *proto.Error) {
		return nil, &proto.Error{Code: proto.CodeVersionMismatch, Message: "server speaks version 9"}
	})
	var dials atomic.Int32
	c.after = (&immediate{}).after
	c.dial = func(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		dials.Add(1)
		return defaultDial(ctx, addr, cfg)
	}
	drain(t, c)
	start(t, c)
	waitState(t, c, connector.StateVersionMismatch)
	time.Sleep(100 * time.Millisecond)
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
}

func TestUnknownKeyBecomesNotAuthorised(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, dir := e.joined(t, "ann")
	if err := e.h.Store.RemoveKey(fingerprint(t, dir, e.addr)); err != nil {
		t.Fatal(err)
	}
	drain(t, c)
	start(t, c)
	waitState(t, c, connector.StateNotAuthorised)
}

func TestRunWithoutJoinIsNotAuthorisedAndDoesNotDial(t *testing.T) {
	var dials atomic.Int32
	c := New(accountFor("127.0.0.1:1"), t.TempDir())
	c.dial = func(context.Context, string, *ssh.ClientConfig) (*ssh.Client, error) {
		dials.Add(1)
		return nil, errors.New("must not dial")
	}
	drain(t, c)
	start(t, c)
	waitState(t, c, connector.StateNotAuthorised)
	if dials.Load() != 0 {
		t.Fatal("dialled without a device key")
	}
}

func TestSendWhileOfflineReturnsErrOffline(t *testing.T) {
	c := New(accountFor("127.0.0.1:1"), t.TempDir())
	began := time.Now()
	_, err := c.Send(context.Background(), "c", "m", text("x"), nil)
	if !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("err = %v, want ErrOffline", err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("offline Send waited")
	}
	if errors.Is(err, connector.ErrUnknownOutcome) {
		t.Fatalf("a request that was never sent reported an unknown outcome: %v", err)
	}
	if c.State() != connector.StateOffline {
		t.Fatalf("State = %s", c.State())
	}
}

func TestStateChangesAreAnnounced(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	start(t, c)
	for i, want := range []connector.ConnState{connector.StateConnecting, connector.StateConnected} {
		select {
		case u := <-c.Updates():
			if u.Event != nil || u.Read != nil || u.Conv != nil {
				t.Fatalf("update %d carries a payload: %+v", i, u)
			}
		case <-time.After(wait):
			t.Fatalf("no update announcing %s", want)
		}
	}
	if c.State() != connector.StateConnected {
		t.Fatalf("State = %s after two announcements", c.State())
	}
	if c.Me().Handle != "ann" {
		t.Fatalf("Me = %+v", c.Me())
	}
}

// pair returns two connected clients, ann (admin) and bob, both in an open room.
func pair(t *testing.T, e *env) (ann, bob *Client, room model.Conversation) {
	t.Helper()
	e.person(t, "ann", true)
	e.person(t, "bob", false)
	ann, bob = e.client(t, "ann"), e.client(t, "bob")
	room, err := ann.Room(context.Background(), connector.RoomOp{Kind: connector.RoomCreate, Name: "dev", Open: true})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := bob.Room(context.Background(), connector.RoomOp{Kind: connector.RoomJoin, Conv: room.ID}); err != nil {
		t.Fatalf("join room: %v", err)
	}
	return ann, bob, room
}

func TestLivePushArrivesOnUpdates(t *testing.T) {
	e := newEnv(t)
	ann, bob, room := pair(t, e)
	if _, err := bob.Send(context.Background(), room.ID, "m1", text("hello"), []string{"ann"}); err != nil {
		t.Fatal(err)
	}
	u := waitUpdate(t, ann, isEvent(model.MessagePosted, "m1"))
	ev := u.Event
	if ev.ConvID != room.ID || ev.Actor != "bob" || string(ev.Body.Data) != "hello" || !slices.Equal(ev.Mentions, []string{"ann"}) {
		t.Fatalf("event = %+v", ev)
	}
}

func TestOtherDeviceReceivesReadAndConvPushes(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	a1, a2 := e.client(t, "ann"), e.client(t, "ann")
	room, err := a1.Room(context.Background(), connector.RoomOp{Kind: connector.RoomCreate, Name: "solo"})
	if err != nil {
		t.Fatal(err)
	}
	u := waitUpdate(t, a2, func(u connector.Update) bool { return u.Conv != nil })
	if u.Conv.ID != room.ID || u.Conv.Name != "solo" {
		t.Fatalf("conv update = %+v", u.Conv)
	}
	ev, err := a1.Send(context.Background(), room.ID, "m1", text("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a1.MarkRead(context.Background(), room.ID, ev.Seq); err != nil {
		t.Fatal(err)
	}
	u = waitUpdate(t, a2, func(u connector.Update) bool { return u.Read != nil })
	if u.Read.ConvID != room.ID || u.Read.Seq != ev.Seq {
		t.Fatalf("read update = %+v", u.Read)
	}
}

func TestEveryMethodRoundTrips(t *testing.T) {
	e := newEnv(t)
	ann, bob, room := pair(t, e)
	ctx := context.Background()

	people, err := ann.People(ctx)
	if err != nil || len(people) != 2 {
		t.Fatalf("People = %+v, %v", people, err)
	}

	// Send, duplicate, edit, delete.
	ev, err := ann.Send(ctx, room.ID, "m1", text("one"), nil)
	if err != nil || ev.Kind != model.MessagePosted || string(ev.Body.Data) != "one" {
		t.Fatalf("Send = %+v, %v", ev, err)
	}
	dup, err := ann.Send(ctx, room.ID, "m1", text("one"), nil)
	if err != nil || dup.Seq != ev.Seq {
		t.Fatalf("duplicate Send = %+v, %v; want the original (seq %d)", dup, err, ev.Seq)
	}
	ed, err := ann.Edit(ctx, room.ID, "m1", text("uno"), nil)
	if err != nil || ed.Kind != model.MessageEdited || string(ed.Body.Data) != "uno" || ed.Seq <= ev.Seq {
		t.Fatalf("Edit = %+v, %v", ed, err)
	}
	del, err := ann.Delete(ctx, room.ID, "m1")
	if err != nil || del.Kind != model.MessageDeleted || del.MsgID != "m1" {
		t.Fatalf("Delete = %+v, %v", del, err)
	}
	waitUpdate(t, bob, isEvent(model.MessageDeleted, "m1"))

	// History pages backwards.
	for i := range 4 {
		if _, err := ann.Send(ctx, room.ID, fmt.Sprintf("h%d", i), text("x"), nil); err != nil {
			t.Fatal(err)
		}
	}
	newest, err := ann.History(ctx, room.ID, 0, 2)
	if err != nil || len(newest) != 2 || newest[1].MsgID != "h3" || newest[0].MsgID != "h2" {
		t.Fatalf("History newest = %+v, %v", newest, err)
	}
	older, err := ann.History(ctx, room.ID, newest[0].Seq, 2)
	if err != nil || len(older) != 2 || older[1].Seq != newest[0].Seq-1 {
		t.Fatalf("History older = %+v, %v", older, err)
	}

	// MarkRead is reflected in Conversations.
	if err := ann.MarkRead(ctx, room.ID, newest[1].Seq); err != nil {
		t.Fatal(err)
	}
	mine, open, err := ann.Conversations(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("Conversations = %+v / %+v, %v", mine, open, err)
	}
	if i := slices.IndexFunc(mine, func(c model.Conversation) bool { return c.ID == room.ID }); i < 0 || mine[i].ReadSeq != newest[1].Seq {
		t.Fatalf("room in Conversations = %+v, want ReadSeq %d", mine, newest[1].Seq)
	}

	// DM.
	dm, err := ann.OpenDM(ctx, "bob")
	if err != nil || dm.Kind != model.ConvDM || !slices.Contains(dm.Members, "bob") {
		t.Fatalf("OpenDM = %+v, %v", dm, err)
	}

	// Room operations.
	e.person(t, "cy", false)
	renamed, err := ann.Room(ctx, connector.RoomOp{Kind: connector.RoomRename, Conv: room.ID, Name: "dev2"})
	if err != nil || renamed.Name != "dev2" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	invited, err := ann.Room(ctx, connector.RoomOp{Kind: connector.RoomInvite, Conv: room.ID, Handle: "cy"})
	if err != nil || !slices.Contains(invited.Members, "cy") {
		t.Fatalf("invite = %+v, %v", invited, err)
	}
	removed, err := ann.Room(ctx, connector.RoomOp{Kind: connector.RoomRemove, Conv: room.ID, Handle: "cy"})
	if err != nil || slices.Contains(removed.Members, "cy") {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	left, err := bob.Room(ctx, connector.RoomOp{Kind: connector.RoomLeave, Conv: room.ID})
	if err != nil || left.ID != room.ID {
		t.Fatalf("leave = %+v, %v", left, err)
	}
	_, open, err = bob.Conversations(ctx)
	if err != nil || len(open) != 1 || open[0].ID != room.ID {
		t.Fatalf("bob's open rooms after leaving = %+v, %v", open, err)
	}
	if _, err := ann.Room(ctx, connector.RoomOp{Kind: "bogus"}); !errors.Is(err, connector.ErrInvalid) {
		t.Fatalf("bogus room op err = %v, want ErrInvalid", err)
	}

	// Invites.
	for name, f := range map[string]func() (string, error){
		"DeviceInvite": func() (string, error) { return ann.DeviceInvite(ctx) },
		"CreateAgent":  func() (string, error) { return ann.CreateAgent(ctx, "bot", "Bot") },
		"AdminInvite":  func() (string, error) { return ann.AdminInvite(ctx, "dee", "Dee") },
	} {
		if code, err := f(); err != nil || code == "" {
			t.Fatalf("%s = %q, %v", name, code, err)
		}
	}
	if _, err := bob.AdminInvite(ctx, "eve", "Eve"); !errors.Is(err, connector.ErrForbidden) {
		t.Fatalf("AdminInvite by non-admin err = %v, want ErrForbidden", err)
	}
}

func TestSyncPagesAndReportsGone(t *testing.T) {
	e := newEnv(t)
	ann, bob, room := pair(t, e)
	ctx := context.Background()

	// Bodies of 8000 bytes push the response past the server's size budget.
	big := model.Body{Format: "text", Data: []byte(strings.Repeat("x", 8000))}
	const total = 80
	for i := range total {
		if _, err := ann.Send(ctx, room.ID, fmt.Sprintf("b%02d", i), big, nil); err != nil {
			t.Fatal(err)
		}
	}
	cursors := map[string]int64{room.ID: 0}
	var got []model.Event
	pages := 0
	for {
		evs, more, gone, err := bob.Sync(ctx, cursors)
		if err != nil || len(gone) != 0 {
			t.Fatalf("Sync = %d events, gone %v, %v", len(evs), gone, err)
		}
		pages++
		got = append(got, evs...)
		if len(evs) > 0 {
			cursors[room.ID] = evs[len(evs)-1].Seq
		}
		if !more {
			break
		}
		if pages > 50 {
			t.Fatal("Sync never finished")
		}
	}
	if pages < 2 {
		t.Fatalf("Sync finished in %d page; the test needs more=true at least once", pages)
	}
	var posted int
	for i, ev := range got {
		if i > 0 && ev.Seq != got[i-1].Seq+1 {
			t.Fatalf("sync gap between seq %d and %d", got[i-1].Seq, ev.Seq)
		}
		if ev.Kind == model.MessagePosted {
			posted++
		}
	}
	if posted != total {
		t.Fatalf("synced %d messages, want %d", posted, total)
	}

	if _, err := bob.Room(ctx, connector.RoomOp{Kind: connector.RoomLeave, Conv: room.ID}); err != nil {
		t.Fatal(err)
	}
	_, _, gone, err := bob.Sync(ctx, map[string]int64{room.ID: 0})
	if err != nil || !slices.Equal(gone, []string{room.ID}) {
		t.Fatalf("Sync after leaving: gone = %v, %v", gone, err)
	}
}

func TestErrorCodesMapToSentinels(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c := e.client(t, "ann")
	cases := map[string]error{
		proto.CodeNotAuthorised:   connector.ErrNotAuthorised,
		proto.CodeForbidden:       connector.ErrForbidden,
		proto.CodeNotFound:        connector.ErrNotFound,
		proto.CodeInvalid:         connector.ErrInvalid,
		proto.CodeTooLarge:        connector.ErrTooLarge,
		proto.CodeRateLimited:     connector.ErrRateLimited,
		proto.CodeVersionMismatch: connector.ErrVersionMismatch,
		proto.CodeInternal:        connector.ErrInternal,
		"something_new":           connector.ErrInternal,
	}
	for code, want := range cases {
		e.h.Server.Handle(proto.TypePeopleList, func(context.Context, *server.Session, proto.Frame) (any, *proto.Error) {
			return nil, &proto.Error{Code: code, Message: "msg for " + code}
		})
		_, err := c.People(context.Background())
		if !errors.Is(err, want) {
			t.Errorf("code %s: err = %v, want %v", code, err, want)
		}
		if err == nil || !strings.Contains(err.Error(), "msg for "+code) {
			t.Errorf("code %s: %v lost the server's message", code, err)
		}
	}
}

func TestServerErrorsFromRealHandlers(t *testing.T) {
	e := newEnv(t)
	ann, _, room := pair(t, e)
	ctx := context.Background()
	if _, err := ann.History(ctx, "no-such-conv", 0, 10); !errors.Is(err, connector.ErrNotFound) {
		t.Errorf("History unknown conv: %v, want ErrNotFound", err)
	}
	if _, err := ann.Send(ctx, room.ID, "m", model.Body{Format: "text", Data: make([]byte, 9000)}, nil); !errors.Is(err, connector.ErrTooLarge) {
		t.Errorf("oversized Send: %v, want ErrTooLarge", err)
	}
	if _, err := ann.Send(ctx, room.ID, "", text("x"), nil); !errors.Is(err, connector.ErrInvalid) {
		t.Errorf("Send with empty msgID: %v, want ErrInvalid", err)
	}
}

// blockOn makes requests of type typ wait until release is called and reports
// when one arrives.
func blockOn(e *env, typ string) (arrived <-chan struct{}, release func()) {
	in, rel := make(chan struct{}, 8), make(chan struct{})
	e.h.Server.Handle(typ, func(ctx context.Context, _ *server.Session, _ proto.Frame) (any, *proto.Error) {
		in <- struct{}{}
		select {
		case <-rel:
		case <-ctx.Done():
		}
		return proto.PeopleListRes{}, nil
	})
	var once sync.Once
	return in, func() { once.Do(func() { close(rel) }) }
}

func TestInFlightRequestReturnsErrOffline(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c := e.client(t, "ann")
	drain(t, c)
	arrived, release := blockOn(e, proto.TypePeopleList)
	defer release()

	errc := make(chan error, 1)
	go func() {
		_, err := c.People(context.Background())
		errc <- err
	}()
	<-arrived
	stopped := make(chan struct{})
	go func() { e.h.StopServer(); close(stopped) }()
	select {
	case err := <-errc:
		if !errors.Is(err, connector.ErrOffline) {
			t.Fatalf("err = %v, want ErrOffline", err)
		}
	case <-time.After(wait):
		t.Fatal("request did not return when the server stopped")
	}
	release()
	<-stopped
}

func TestRequestTimesOutAndHonoursContext(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c := e.client(t, "ann", func(c *Client) { c.timeout = 100 * time.Millisecond })
	drain(t, c)
	_, release := blockOn(e, proto.TypePeopleList)
	defer release()

	_, err := c.People(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout wrapping DeadlineExceeded", err)
	}
	if errors.Is(err, connector.ErrUnknownOutcome) {
		t.Fatalf("a read-only request reported an unknown outcome: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.People(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: err = %v, want context.Canceled", err)
	}
}

func TestConcurrentRequests(t *testing.T) {
	e := newEnv(t)
	ann, _, room := pair(t, e)
	drain(t, ann)
	const workers, each = 8, 10
	seqs := make(chan int64, workers*each)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				ev, err := ann.Send(context.Background(), room.ID, fmt.Sprintf("w%d-%d", w, i), text("x"), nil)
				if err != nil {
					t.Errorf("Send: %v", err)
					return
				}
				seqs <- ev.Seq
			}
		}()
	}
	wg.Wait()
	close(seqs)
	seen := map[int64]bool{}
	for s := range seqs {
		if seen[s] {
			t.Fatalf("seq %d returned twice", s)
		}
		seen[s] = true
	}
	if len(seen) != workers*each {
		t.Fatalf("%d distinct seqs, want %d", len(seen), workers*each)
	}
}

func TestSlowConsumerLosesNoPush(t *testing.T) {
	e := newEnv(t)
	ann, bob, room := pair(t, e)
	const n = 400 // more than the Updates buffer, so the reader must block
	for i := range n {
		if _, err := bob.Send(context.Background(), room.ID, fmt.Sprintf("m%03d", i), text("x"), nil); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for len(got) < n {
		var u connector.Update
		select {
		case u = <-ann.Updates():
		case <-time.After(wait):
			t.Fatalf("received %d of %d messages, then nothing", len(got), n)
		}
		if u.Event != nil && u.Event.Kind == model.MessagePosted {
			got = append(got, u.Event.MsgID)
			if len(got) < 20 {
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	for i, id := range got {
		if want := fmt.Sprintf("m%03d", i); id != want {
			t.Fatalf("message %d = %s, want %s", i, id, want)
		}
	}
	if ann.State() != connector.StateConnected {
		t.Fatalf("State = %s; the session should not have been dropped", ann.State())
	}
}

func TestRunReturnsWhileConsumerIsStalled(t *testing.T) {
	e := newEnv(t)
	ann, bob, room := pair(t, e)
	for i := range 300 {
		if _, err := bob.Send(context.Background(), room.ID, fmt.Sprintf("m%03d", i), text("x"), nil); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(wait)
	for len(ann.updates) < cap(ann.updates) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(ann.updates) != cap(ann.updates) {
		t.Fatalf("buffer holds %d of %d", len(ann.updates), cap(ann.updates))
	}
	// Cleanup cancels Run while the reader is blocked delivering; start's
	// cleanup fails the test if Run does not return.
}

func TestHelloReturningPersonSetsMe(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", true)
	c := e.client(t, "ann")
	if me := c.Me(); me.Handle != "ann" || !me.Admin {
		t.Fatalf("Me = %+v", me)
	}
	if caps := c.Caps(); !(caps.Edit && caps.Delete && caps.Rooms && caps.Agents) {
		t.Fatalf("Caps = %+v", caps)
	}
}

func TestServerKeyIsThePinnedFingerprint(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	dir := t.TempDir()
	if got := New(config.Account{Name: "ann", Server: e.addr, Handle: "ann"}, dir).ServerKey(); got != "" {
		t.Fatalf("ServerKey before join = %q, want empty", got)
	}
	code, _ := e.h.Store.CreateDeviceInvite("ann", server.InviteTTL)
	acct, err := Join(context.Background(), e.addr, code, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := ssh.FingerprintSHA256(e.h.Server.HostKey())
	if got := New(acct, dir).ServerKey(); got != want {
		t.Fatalf("ServerKey = %q, want %q", got, want)
	}
}
