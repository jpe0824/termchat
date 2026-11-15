package daemon

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

func TestCLISendDenied(t *testing.T) {
	e := newEnv(t)
	e.cfg.CLI.Send = "deny"
	c := startWithRoom(t, e)
	if _, err := c.Send("r1", "from a script", "cli"); !errors.Is(err, ipc.ErrSendDenied) {
		t.Fatalf("cli Send = %v, want ErrSendDenied", err)
	}
	if items := e.outbox("r1"); len(items) != 0 {
		t.Fatalf("denied send queued %+v", items)
	}
	if _, err := c.Send("r1", "typed by hand", "tui"); err != nil {
		t.Fatalf("tui Send = %v", err)
	}
	waitUntil(t, "the tui message", func() bool { return slices.Equal(sentTexts(e), []string{"typed by hand"}) })
}

func TestCLISendAllowedByDefault(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	if _, err := c.Send("r1", "from a script", "cli"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the message", func() bool { return len(e.fake.Sent()) == 1 })
}

func unread(t *testing.T, c *ipc.Client, conv string) int {
	t.Helper()
	mine, _, err := c.Chats()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range mine {
		if v.ID == conv {
			return v.Unread
		}
	}
	t.Fatalf("conversation %s not listed", conv)
	return 0
}

func TestCLIReadDoesNotMoveReadPosition(t *testing.T) {
	e := newEnv(t)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 3)...)
	c := e.start()
	waitUntil(t, "three unread", func() bool { return len(chatIDs(t, c)) == 1 && unread(t, c, "r1") == 3 })

	if msgs, err := c.Messages("r1", 0, 50); err != nil || len(msgs) != 3 {
		t.Fatalf("Messages = %d, %v", len(msgs), err)
	}
	if _, err := c.Search("message", 10); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, c, "r1"); n != 3 {
		t.Fatalf("Unread after reading = %d, want 3", n)
	}
	if err := c.MarkRead("r1"); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, c, "r1"); n != 0 {
		t.Fatalf("Unread after MarkRead = %d, want 0", n)
	}
	mine, _, _ := e.fake.Conversations(context.Background())
	if mine[0].ReadSeq != 3 {
		t.Fatalf("server read position = %d, want 3", mine[0].ReadSeq)
	}
	if err := c.MarkRead("nope"); !errors.Is(err, ipc.ErrNotFound) {
		t.Fatalf("MarkRead(unknown) = %v, want ErrNotFound", err)
	}
}

func TestResolveRefs(t *testing.T) {
	e := newEnv(t)
	e.fake.AddPeople(model.Person{Handle: "dana"}, model.Person{Handle: "platform"})
	e.fake.AddConversation(room("r1", "platform", "me", "dana"))
	e.fake.AddConversation(room("r2", "design", "me"))
	e.fake.AddConversation(model.Conversation{ID: "dm:dana:me", Kind: model.ConvDM, Members: []string{"dana", "me"}})
	e.fake.AddConversation(model.Conversation{ID: "dm:me:platform", Kind: model.ConvDM, Members: []string{"me", "platform"}})
	open := room("r3", "lobby", "dana")
	open.Open = true
	e.fake.AddConversation(open)
	c := e.start()
	waitUntil(t, "four conversations", func() bool { return len(chatIDs(t, c)) == 4 })

	for ref, want := range map[string]string{
		"@Dana": "dm:dana:me", "@dana": "dm:dana:me", "#platform": "r1", "#Design": "r2",
		"r2": "r2", "dm:dana:me": "dm:dana:me", "design": "r2", "dana": "dm:dana:me", "#lobby": "r3",
	} {
		got, err := c.Resolve(ref)
		if err != nil || got.ID != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got.ID, err, want)
		}
	}
	for _, ref := range []string{"@nobody", "#nowhere", "nothing", "@design"} {
		if _, err := c.Resolve(ref); !errors.Is(err, ipc.ErrNotFound) {
			t.Errorf("Resolve(%q) = %v, want ErrNotFound", ref, err)
		}
	}
	// "platform" is both a room and a person with a DM.
	if _, err := c.Resolve("platform"); !errors.Is(err, ipc.ErrAmbiguous) {
		t.Errorf("Resolve(platform) = %v, want ErrAmbiguous", err)
	}
	if _, open, _ := c.Chats(); len(open) != 1 || open[0].ID != "r3" {
		t.Errorf("open rooms = %+v", open)
	}
}

// unanswered makes Edit, Delete and Room report an unknown outcome, after
// performing the change when apply is true.
type unanswered struct {
	*recorder
	apply atomic.Bool
}

func (u *unanswered) Edit(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	if u.apply.Load() {
		if _, err := u.recorder.Edit(ctx, conv, msgID, body, mentions); err != nil {
			return model.Event{}, err
		}
	}
	return model.Event{}, connector.ErrUnknownOutcome
}

func (u *unanswered) Delete(ctx context.Context, conv, msgID string) (model.Event, error) {
	if u.apply.Load() {
		if _, err := u.recorder.Delete(ctx, conv, msgID); err != nil {
			return model.Event{}, err
		}
	}
	return model.Event{}, connector.ErrUnknownOutcome
}

func (u *unanswered) Room(ctx context.Context, op connector.RoomOp) (model.Conversation, error) {
	if u.apply.Load() {
		if _, err := u.recorder.Room(ctx, op); err != nil {
			return model.Conversation{}, err
		}
	}
	return model.Conversation{}, connector.ErrUnknownOutcome
}

func TestEditAndDelete(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	id, _ := c.Send("r1", "frist", "tui")
	waitUntil(t, "the message", func() bool { return len(e.outbox("r1")) == 0 })

	if err := c.Edit("r1", id, "first"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := c.Messages("r1", 0, 10)
	if len(msgs) != 1 || msgs[0].Text != "first" || !msgs[0].Edited {
		t.Fatalf("after Edit: %+v", msgs)
	}
	if err := c.Edit("r1", id, " "); !errors.Is(err, ipc.ErrInvalid) {
		t.Fatalf("empty Edit = %v, want ErrInvalid", err)
	}
	e.goOffline(c)
	if err := c.Edit("r1", id, "offline"); !errors.Is(err, ipc.ErrNotConnected) {
		t.Fatalf("offline Edit = %v, want ErrNotConnected", err)
	}
	if err := c.Delete("r1", id); !errors.Is(err, ipc.ErrNotConnected) {
		t.Fatalf("offline Delete = %v, want ErrNotConnected", err)
	}
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "online", func() bool { st, _ := c.Status(); return st.Conn == connector.StateConnected })
	if err := c.Delete("r1", id); err != nil {
		t.Fatal(err)
	}
	msgs, _ = c.Messages("r1", 0, 10)
	if len(msgs) != 1 || !msgs[0].Deleted {
		t.Fatalf("after Delete: %+v", msgs)
	}
}

func TestUnknownOutcomeIsCheckedNotReportedAsFailure(t *testing.T) {
	e := newEnv(t)
	u := &unanswered{recorder: record(e)}
	e.conn = u
	c := startWithRoom(t, e)
	id, _ := c.Send("r1", "draft", "tui")
	waitUntil(t, "the message", func() bool { return len(e.outbox("r1")) == 0 })

	// The change happened: the follow-up sync shows it and the call succeeds.
	u.apply.Store(true)
	if err := c.Edit("r1", id, "final"); err != nil {
		t.Fatalf("Edit whose answer was lost but which took effect = %v", err)
	}
	if msgs, _ := c.Messages("r1", 0, 10); msgs[0].Text != "final" {
		t.Fatalf("after Edit: %+v", msgs)
	}
	if _, err := c.Room(connector.RoomOp{Kind: connector.RoomRename, Conv: "r1", Name: "renamed"}); err != nil {
		t.Fatalf("rename whose answer was lost but which took effect = %v", err)
	}

	// The change did not happen: reported as a timeout saying so.
	u.apply.Store(false)
	err := c.Delete("r1", id)
	if !errors.Is(err, ipc.ErrTimeout) {
		t.Fatalf("Delete that did not take effect = %v, want ErrTimeout", err)
	}
	if msgs, _ := c.Messages("r1", 0, 10); msgs[0].Deleted {
		t.Fatal("message shown deleted although the server did not delete it")
	}
	if _, err := c.Room(connector.RoomOp{Kind: connector.RoomLeave, Conv: "r1"}); !errors.Is(err, ipc.ErrTimeout) {
		t.Fatalf("leave that did not take effect = %v, want ErrTimeout", err)
	}
	u.apply.Store(true)
	if _, err := c.Room(connector.RoomOp{Kind: connector.RoomLeave, Conv: "r1"}); err != nil {
		t.Fatalf("leave that took effect = %v", err)
	}
	waitUntil(t, "r1 gone", func() bool { return len(chatIDs(t, c)) == 0 })
}

func TestRoomOperations(t *testing.T) {
	e := newEnv(t)
	e.fake.AddPeople(model.Person{Handle: "dana"})
	lobby := room("lobby", "lobby", "dana")
	lobby.Open = true
	e.fake.AddConversation(lobby, posted("dana", "l1", "welcome"))
	c := e.start()
	waitUntil(t, "the open room", func() bool { _, open, _ := c.Chats(); return len(open) == 1 })

	created, err := c.Room(connector.RoomOp{Kind: connector.RoomCreate, Name: "ops"})
	if err != nil || created.Name != "ops" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if _, err := c.Room(connector.RoomOp{Kind: connector.RoomJoin, Conv: "lobby"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "both rooms listed", func() bool { return len(chatIDs(t, c)) == 2 })
	if msgs, _ := c.Messages("lobby", 0, 10); len(msgs) != 1 || msgs[0].Text != "welcome" {
		t.Fatalf("joined room's history = %+v", msgs)
	}
	if _, open, _ := c.Chats(); len(open) != 0 {
		t.Fatalf("joined room still listed as open: %+v", open)
	}
	dm, err := c.OpenDM("@Dana")
	if err != nil || dm.Kind != model.ConvDM {
		t.Fatalf("OpenDM = %+v, %v", dm, err)
	}
	if got, err := c.Resolve("@dana"); err != nil || got.ID != dm.ID {
		t.Fatalf("Resolve(@dana) after OpenDM = %+v, %v", got, err)
	}
	if code, err := c.DeviceInvite(); err != nil || code == "" {
		t.Fatalf("DeviceInvite = %q, %v", code, err)
	}
	if _, err := c.AdminInvite("x", "X"); !errors.Is(err, ipc.ErrForbidden) {
		t.Fatalf("AdminInvite as non-admin = %v, want ErrForbidden", err)
	}
}

func TestPeopleListsTheStore(t *testing.T) {
	e := newEnv(t)
	e.fake.AddPeople(model.Person{Handle: "dana", DisplayName: "Dana D"},
		model.Person{Handle: "ci-bot", Agent: true, Owner: "dana"})
	c := startWithRoom(t, e)
	want := []model.Person{{Handle: "ci-bot", Agent: true, Owner: "dana"}, {Handle: "dana", DisplayName: "Dana D"}}
	var got []model.Person
	waitUntil(t, "the people list", func() bool {
		var err error
		got, err = c.People()
		if err != nil {
			t.Fatal(err)
		}
		return len(got) >= 2
	})
	var others []model.Person
	for _, p := range got {
		if p.Handle != "me" {
			others = append(others, p)
		}
	}
	if !slices.EqualFunc(others, want, func(a, b model.Person) bool { return a == b }) {
		t.Fatalf("People = %+v, want %+v", got, want)
	}
}
