package tui

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

func TestEveryDispatchingRowRuns(t *testing.T) {
	for _, g := range keyTable {
		for _, b := range g.binds {
			if len(b.match) > 0 && b.run == nil {
				t.Errorf("%s: %q matches %v but runs nothing", g.name, b.keys, b.match)
			}
			if len(b.match) == 0 && (b.run != nil || b.kind != keyImmediate) {
				t.Errorf("%s: %q only documents a key, yet carries a run or a kind", g.name, b.keys)
			}
		}
	}
}

// The thread's own command keys, pressed with the chat list focused, go
// through the guard as they do in the thread (esc is immediate there), and
// then do nothing: no hint, no banner, no write.
func TestThreadKeysAreInertInTheChatList(t *testing.T) {
	for _, k := range []string{"e", "d", "x", "i", "esc"} {
		t.Run(k, func(t *testing.T) {
			f := fixture()
			h := opened(t, f, 90, 20)
			h.key("up")
			h.key("esc")
			h.key("esc")
			if h.m.focus != focusList || !h.m.listActive(h.m.geom()) {
				t.Fatalf("focus %v, want the chat list", h.m.focus)
			}
			f.resetCalls()
			before := snapshot(h)
			h.step = time.Millisecond
			h.key(k)
			if held := h.m.tm.pending != nil && h.m.tm.pending.bound; held != (k != "esc") {
				t.Errorf("%s held as a guarded key: %v", k, held)
			}
			h.step = time.Second
			h.settle()
			if after := snapshot(h); !reflect.DeepEqual(after, before) {
				t.Errorf("%s changed the state:\n%+v\nwant\n%+v", k, after, before)
			}
			if h.m.tm.banner.text != "" || h.m.note != nil {
				t.Errorf("%s left banner %q note %+v", k, h.m.tm.banner.text, h.m.note)
			}
			if w := f.writes(everyWrite...); w != nil {
				t.Errorf("%s wrote %q", k, w)
			}
		})
	}
}

// Below the minimum window size the input is not drawn, so keys and pastes
// meant for it are dropped rather than typed unseen; the input keeps the
// keyboard for when the window grows again.
func TestTooSmallWindowTypesNothing(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("ab")
	h.size(15, 4)
	h.typeText("cd")
	h.key("backspace")
	h.paste("ef")
	h.enter()
	if h.draft() != "ab" || h.m.focus != focusInput || h.m.note != nil {
		t.Errorf("draft %q focus %v note %+v, want \"ab\" kept, the input focused, no note", h.draft(), h.m.focus,
			h.m.note)
	}
	if h.m.View().Cursor != nil {
		t.Error("a cursor is shown in an input that is not drawn")
	}
	if w := f.writes(everyWrite...); w != nil {
		t.Errorf("wrote %q", w)
	}
	h.size(90, 20)
	h.typeText("g")
	if h.draft() != "abg" {
		t.Errorf("draft %q, want \"abg\"", h.draft())
	}
}

// A picker's footer names the person enter acts on, cut short so a long
// handle leaves the footer its labels at narrow widths.
func TestPickerFooterKeepsItsLabelsWithALongHandle(t *testing.T) {
	long := strings.Repeat("l", 32)
	for _, c := range []struct {
		w    int
		want []string
	}{
		{44, []string{"⏎ message llllllllll", "esc back"}},
		{30, []string{"⏎ message llllllllll"}},
	} {
		t.Run(strconv.Itoa(c.w), func(t *testing.T) {
			f := peopleFixture()
			f.ov.people = append(f.ov.people, model.Person{Handle: long})
			h := opened(t, f, c.w, 20)
			h.key("esc")
			h.key("ctrl+n")
			h.key("enter")
			h.typeText(long)
			if h.m.ov == nil || h.m.ov.step != stepDM {
				t.Fatalf("not in the direct message picker: %s", ovState(h))
			}
			foot := h.lines()[len(h.lines())-1]
			for _, w := range c.want {
				if !strings.Contains(foot, w) {
					t.Errorf("footer %q lacks %q", foot, w)
				}
			}
			if strings.Contains(foot, long) {
				t.Errorf("footer %q holds the whole handle", foot)
			}
		})
	}
}
