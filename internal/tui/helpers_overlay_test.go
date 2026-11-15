package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// overlayData is what the fake serves to the overlays: the people list, the
// open rooms not joined, and search results by query.
type overlayData struct {
	people    []model.Person
	openRooms []model.Conversation
	results   map[string][]ipc.Message
}

func (f *fakeBackend) People() ([]model.Person, error) {
	f.record("people")
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ov.people), f.errs["people"]
}

func (f *fakeBackend) Search(q string, limit int) ([]ipc.Message, error) {
	f.record(fmt.Sprintf("search %q %d", q, limit))
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ov.results[q]), f.errs["search"]
}

// OpenDM answers with dm-<handle>, which the next Chats lists.
func (f *fakeBackend) OpenDM(handle string) (model.Conversation, error) {
	f.record("opendm " + handle)
	if err := f.errFor("opendm"); err != nil {
		return model.Conversation{}, err
	}
	c := model.Conversation{ID: "dm-" + handle, Kind: model.ConvDM, Members: []string{"jason", handle}}
	f.mu.Lock()
	f.convs = append(f.convs, ipc.ConvView{Conversation: c})
	f.mu.Unlock()
	return c, nil
}

// Room records the operation as "room <kind> <conv> <name> <handle> <open>"
// and applies it to the conversations the fake lists.
func (f *fakeBackend) Room(op ipc.RoomOp) (model.Conversation, error) {
	f.record(fmt.Sprintf("room %s %s %s %s %v", op.Kind, op.Conv, op.Name, op.Handle, op.Open))
	if err := f.errFor("room"); err != nil {
		return model.Conversation{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if op.Kind == ipc.RoomCreate {
		c := model.Conversation{ID: "r-" + op.Name, Kind: model.ConvRoom, Name: op.Name, Open: op.Open,
			Creator: "jason", Members: []string{"jason"}}
		f.convs = append(f.convs, ipc.ConvView{Conversation: c})
		return c, nil
	}
	if op.Kind == ipc.RoomJoin {
		for i, c := range f.ov.openRooms {
			if c.ID == op.Conv {
				c.Members = append(c.Members, "jason")
				f.ov.openRooms = slices.Delete(f.ov.openRooms, i, i+1)
				f.convs = append(f.convs, ipc.ConvView{Conversation: c})
				return c, nil
			}
		}
		return model.Conversation{}, ipc.ErrNotFound
	}
	for i := range f.convs {
		c := &f.convs[i].Conversation
		if c.ID != op.Conv {
			continue
		}
		switch op.Kind {
		case ipc.RoomInvite:
			c.Members = append(slices.Clone(c.Members), op.Handle)
		case ipc.RoomRemove:
			c.Members = slices.DeleteFunc(slices.Clone(c.Members), func(h string) bool { return h == op.Handle })
		case ipc.RoomRename:
			c.Name = op.Name
		case ipc.RoomLeave:
			out := *c
			f.convs = slices.Delete(f.convs, i, i+1)
			return out, nil
		}
		return *c, nil
	}
	return model.Conversation{}, ipc.ErrNotFound
}

// roomWrites are the recorded calls that change something through an
// overlay.
var roomWrites = []string{"opendm ", "room "}

// people is the fixture's people: everyone it has a DM with, and two it has
// none with yet.
func withPeople(f *fakeBackend) *fakeBackend {
	f.ov.people = []model.Person{
		{Handle: "ci-bot", DisplayName: "CI", Agent: true, Owner: "dana"},
		{Handle: "dana", DisplayName: "Dana Scully"},
		{Handle: "jason", DisplayName: "Jason"},
		{Handle: "mo"},
		{Handle: "priya", DisplayName: "Priya Patel"},
		{Handle: "sam", DisplayName: "Sam Lee"},
	}
	f.ov.openRooms = []model.Conversation{{ID: "r-design", Kind: model.ConvRoom, Name: "design", Open: true,
		Creator: "priya", Members: []string{"priya", "mo"}}}
	return f
}

func peopleFixture() *fakeBackend { return withPeople(fixture()) }

// fireSearch fires the search's waiting timers.
func (h *tester) fireSearch() {
	h.t.Helper()
	var keep, due []tick
	for _, tk := range h.ticks {
		if t, ok := tk.msg.(timerMsg); ok && t.id == timerSearch {
			due = append(due, tk)
		} else {
			keep = append(keep, tk)
		}
	}
	h.ticks = keep
	for _, tk := range due {
		h.send(tk.msg)
	}
}

// rowTexts are the rows the open overlay lists, as drawn.
func rowTexts(h *tester) []string {
	h.t.Helper()
	var out []string
	for _, r := range h.m.ovRows() {
		out = append(out, r.text)
	}
	return out
}

// ovKind is the open overlay and its step, "" for none.
func ovState(h *tester) string {
	if h.m.ov == nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", h.m.ov.kind, h.m.ov.step)
}

// widthsOK fails the test unless the frame has hgt rows of exactly w cells.
func widthsOK(t *testing.T, h *tester, w, hgt int) {
	t.Helper()
	lines := h.lines()
	if len(lines) != hgt {
		t.Errorf("%dx%d: %d rows", w, hgt, len(lines))
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Errorf("%dx%d: row %d is %d cells: %q", w, hgt, i, lw, l)
		}
	}
}

// ovLines are the rows of the open overlay's box, cut out of the frame.
func ovLines(h *tester) []string {
	h.t.Helper()
	r := h.m.ovRect()
	lines := h.lines()
	var out []string
	for y := r.y; y < r.y+r.h && y < len(lines); y++ {
		out = append(out, ansi.Cut(lines[y], r.x, r.x+r.w))
	}
	return out
}

// ovFlat is the overlay's text with every run of whitespace and the box's
// side borders as one space, so wrapped text reads as one line.
func ovFlat(h *tester) string {
	h.t.Helper()
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.Join(ovLines(h), "\n"), "│", " ")), " ")
}

// ovAt is a screen cell on the overlay row that shows text.
func ovAt(h *tester, text string) (x, y int) {
	h.t.Helper()
	r := h.m.ovRect()
	for i, l := range ovLines(h) {
		if strings.Contains(l, text) {
			return r.x + r.w/2, r.y + i
		}
	}
	h.t.Fatalf("no overlay row with %q:\n%s", text, h.view())
	return 0, 0
}
