package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

// overlayViews are the keys that bring each overlay, or one of its steps,
// on screen from a freshly opened # platform the user created.
var overlayViews = []struct {
	name string
	keys []string
}{
	{"jump", []string{"ctrl+k"}},
	{"jump-filtered", []string{"ctrl+k", "p", "r"}},
	{"new", []string{"ctrl+n"}},
	{"new-dm", []string{"ctrl+n", "enter", "s"}},
	{"new-room", []string{"ctrl+n", "down", "enter", "B", "a", "d", " ", "x"}},
	{"new-join", []string{"ctrl+n", "down", "down", "enter"}},
	{"search", []string{"/", "m", "e", "t", "e", "r", "search"}},
	{"room", []string{"ctrl+o"}},
	{"room-invite", []string{"ctrl+o", "a", "p"}},
	{"room-rename", []string{"ctrl+o", "n"}},
	{"room-remove", []string{"ctrl+o", "down", "x"}},
	{"help", []string{"?"}},
}

func showOverlay(t *testing.T, w, hgt int, keys []string) (*fakeBackend, *tester) {
	t.Helper()
	f := managedFixture()
	f.ov.results = map[string][]ipc.Message{"meter": {
		msg("r-platform", 1, 2, "dana", "did the meter import finish?"),
		msg("dm-dana", 2, 44, "dana", "after the long weekend we finally got the meter readings in"),
	}}
	h := opened(t, f, w, hgt)
	if keys[0] == "?" || keys[0] == "/" {
		h.key("esc") // to the chat list: neither types in the input
	}
	for _, k := range keys {
		if k == "search" {
			h.fireSearch()
			continue
		}
		h.key(k)
	}
	return f, h
}

func TestOverlaysFitEverySize(t *testing.T) {
	for _, v := range overlayViews {
		for _, sz := range [][2]int{{120, 24}, {90, 24}, {44, 24}, {30, 24}, {20, 5}, {90, 6}, {36, 7}} {
			t.Run(fmt.Sprintf("%s/%dx%d", v.name, sz[0], sz[1]), func(t *testing.T) {
				_, h := showOverlay(t, sz[0], sz[1], v.keys)
				if h.m.ov == nil && !h.m.help {
					t.Fatalf("nothing open")
				}
				widthsOK(t, h, sz[0], sz[1])
				for _, k := range []string{"down", "pgdown", "up", "end", "home"} {
					h.key(k)
					widthsOK(t, h, sz[0], sz[1])
				}
				for _, s := range [][2]int{{sz[0] - 1, sz[1]}, {sz[0], sz[1] + 3}, {200, 60}} {
					h.size(s[0], s[1])
					if s[0] < minW {
						if v := h.view(); v != tooSmall[:min(len(tooSmall), s[0])] {
							t.Errorf("%dx%d: %q", s[0], s[1], v)
						}
						continue
					}
					widthsOK(t, h, s[0], s[1])
				}
				h.size(sz[0], sz[1])
				if h.m.ov == nil && !h.m.help {
					t.Errorf("resizing closed it")
				}
			})
		}
	}
}

// TestOverlayGoldenFrames keeps one colourless frame per overlay at 90×24.
func TestOverlayGoldenFrames(t *testing.T) {
	for _, v := range overlayViews {
		t.Run(v.name, func(t *testing.T) {
			_, h := showOverlay(t, 90, 24, v.keys)
			got := h.view() + "\n"
			path := filepath.Join("testdata", "ov-"+v.name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("%s differs from %s:\n--- got\n%s--- want\n%s", v.name, path, got, want)
			}
		})
	}
}

func TestSearchMatchIsVisible(t *testing.T) {
	_, h := showOverlay(t, 44, 24, overlayViews[6].keys)
	// The second result's match lies past what fits; the snippet moves to it.
	_, row := lineWith(ovLines(h), "@dana · dana · …")
	if !strings.Contains(row, "meter readings in") {
		t.Errorf("the snippet does not show the match: %q", row)
	}
	if styled := h.m.st.match.Render("meter"); styled == "meter" || !strings.Contains(h.raw(), styled) {
		t.Errorf("the match is not drawn as %q", styled)
	}
}

// --- help ---

func TestHelpListsEveryKeyFromTheTable(t *testing.T) {
	h := started(t, fixture(), 120, 200)
	h.key("?")
	v := strings.Join(strings.Fields(strings.ReplaceAll(h.view(), "│", " ")), " ")
	for _, g := range keyTable {
		mustContain(t, v, g.name)
		for _, b := range g.binds {
			mustContain(t, v, b.keys+" "+b.desc)
		}
	}
	mustContain(t, v, "ctrl+k", "ctrl+n", "ctrl+o", "/ ")
	// The keys the chat list and thread hold to a lone press are the ones
	// the table marks guarded.
	for _, k := range []string{"d", "e", "x", "r", "q", "i", "enter", "tab", "ctrl+k", "ctrl+n", "ctrl+o", "/"} {
		if !tableGuards(k) {
			t.Errorf("%s is not guarded", k)
		}
	}
}

func tableGuards(k string) bool {
	for _, g := range keyTable {
		for _, b := range g.binds {
			if b.kind == keyGuarded && slices.Contains(b.match, k) {
				return true
			}
		}
	}
	return false
}

func TestHelpScrolls(t *testing.T) {
	h := started(t, fixture(), 90, 12)
	h.key("?")
	first := h.lines()[1]
	h.key("down")
	if h.lines()[1] == first {
		t.Error("down did not scroll the help")
	}
	h.key("end")
	g := keyTable[len(keyTable)-1]
	mustContain(t, flat(h), g.binds[len(g.binds)-1].desc)
	h.key("home")
	if h.lines()[1] != first {
		t.Errorf("home: %q, want %q", h.lines()[1], first)
	}
	h.wheel(40, 5, false)
	if h.lines()[1] == first {
		t.Error("the wheel did not scroll the help")
	}
	h.key("?")
	if h.m.help {
		t.Error("? did not close the help")
	}
}

// --- footers ---

func TestFootersShowOverlayKeys(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	want := "└─ ↑↓ move  ⏎ open  tab pane  / search  ctrl+k jump  ctrl+n new  ? help  q quit ─────────┘"
	if got := h.lines()[19]; got != want {
		t.Errorf("standard list footer\n got %q\nwant %q", got, want)
	}
	h.key("tab")
	mustContain(t, h.lines()[19], "/ search", "ctrl+k jump", "ctrl+o room")
	h = started(t, fixture(), 44, 20)
	h.key("esc")
	mustContain(t, h.lines()[19], "/ search")
}

func TestFootersWhileDownOfferNoDeadKeys(t *testing.T) {
	for _, w := range []int{120, 90, 44, 30} {
		h := opened(t, fixture(), w, 20)
		h.key("up")
		h.key("up") // own delivered message selected
		h.send(backendDownMsg{err: ipc.ErrClosed})
		mustNotContain(t, h.lines()[19], "i type", "e edit", "d delete")
		h.key("esc")
		mustNotContain(t, h.lines()[19], "i type", "e edit", "d delete")
	}
}

// --- carried items ---

func TestPreviewPagedOpensOnlyAfterTheGap(t *testing.T) {
	for _, w := range []int{90, 44} {
		for _, k := range []string{"pgup", "pgdown", "home", "end"} {
			f := fixture()
			h := newTester(t, f, markReadOn)
			h.size(w, 8)
			f.resetCalls()
			feed(h)
			keysAt(h, k)
			if !h.m.preview || f.writes("markread ") != nil {
				t.Errorf("%d %s: opened at once: preview %v writes %q", w, k, h.m.preview, f.writes("markread "))
			}
			soon(h, func() { h.key("x") })
			pause(h, time.Second)
			if !h.m.preview || f.writes("markread ") != nil {
				t.Errorf("%d %s then x: preview %v writes %q", w, k, h.m.preview, f.writes("markread "))
			}
			feed(h)
			keysAt(h, k)
			pause(h, time.Second)
			if h.m.preview || !slices.Equal(f.writes("markread "), []string{"markread r-platform"}) {
				t.Errorf("%d %s alone: preview %v writes %q", w, k, h.m.preview, f.writes("markread "))
			}
		}
	}
}

func TestPasteNoteFollowsFocus(t *testing.T) {
	h := newTester(t, fixture())
	h.size(90, 20) // the chat list has the keyboard
	h.paste("hello")
	mustContain(t, flat(h), "paste ignored — press enter to open a chat")
	mustNotContain(t, flat(h), "press i to type")
	h.key("tab") // the thread
	h.paste("hello")
	mustContain(t, flat(h), "paste ignored — press i to type")
}
