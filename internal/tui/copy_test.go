package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/sanitize"
)

func TestCopyKeyCopiesMessageText(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	for range 3 {
		h.key("up")
	}
	h.key("c")
	if want := []string{"@jason can you look?"}; !slices.Equal(h.clips, want) {
		t.Fatalf("clips = %q, want %q", h.clips, want)
	}
	mustContain(t, h.view(), "copied to clipboard (20 characters)")
	h.key("up")
	h.key("c")
	h.key("esc")
	h.key("up")
	h.key("c")
	want := []string{"@jason can you look?", "build 812 passed", "pushing a fix now"}
	if !slices.Equal(h.clips, want) {
		t.Errorf("clips = %q, want %q", h.clips, want)
	}
}

func TestCopyWithoutSelectionDoesNothing(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.key("esc")
	h.key("tab")
	h.key("c")
	if h.clips != nil {
		t.Errorf("clips = %q, want none", h.clips)
	}
	mustNotContain(t, h.view(), "copied")
}

func TestSecondClickCopiesOnlySameMessage(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.click(30, h.row("great. one row"))
	if selectedText(h) != "great. one row looks off" || h.clips != nil {
		t.Fatalf("first click: selected %q, clips %q", selectedText(h), h.clips)
	}
	h.click(30, h.row("great. one row"))
	if want := []string{"great. one row looks off"}; !slices.Equal(h.clips, want) {
		t.Fatalf("second click: clips = %q, want %q", h.clips, want)
	}
	h.click(30, h.row("did the meter"))
	if selectedText(h) != "did the meter import finish?" || len(h.clips) != 1 {
		t.Errorf("click on another message: selected %q, clips %q", selectedText(h), h.clips)
	}
}

func TestSecondClickOnFailedCopiesNotRetries(t *testing.T) {
	f := failedFixture()
	h := opened(t, f, 120, 20)
	h.click(30, h.row("pushing a fix now"))
	h.click(30, h.row("pushing a fix now"))
	if got := f.writes("retry "); !slices.Equal(got, []string{"retry o1"}) {
		t.Errorf("retries = %q, want one", got)
	}
	if want := []string{"pushing a fix now"}; !slices.Equal(h.clips, want) {
		t.Errorf("clips = %q, want %q", h.clips, want)
	}
}

func TestCopyStripsEscapes(t *testing.T) {
	f := fixture()
	text := "run \x1b[31mthis\x1b[0m\x07 now"
	f.addStored("r-platform", msg("r-platform", 7, 46, "dana", text))
	h := opened(t, f, 90, 20)
	h.click(30, h.row("run this"))
	h.key("c")
	if len(h.clips) != 1 || h.clips[0] != sanitize.Text(text) || strings.ContainsAny(h.clips[0], "\x1b\x07") {
		t.Errorf("clips = %q, want %q", h.clips, sanitize.Text(text))
	}
}

func TestCopyKeepsLineBreaksNotWrap(t *testing.T) {
	f := fixture()
	text := strings.Repeat("wrapword ", 20) + "\n" + strings.Repeat("second ", 15)
	f.addStored("r-platform", msg("r-platform", 7, 46, "dana", text))
	h := opened(t, f, 60, 30)
	h.click(30, h.row("wrapword wrapword"))
	h.key("c")
	if len(h.clips) != 1 || h.clips[0] != sanitize.Text(text) {
		t.Errorf("clips = %q, want %q", h.clips, sanitize.Text(text))
	}
}

func TestCopyAfterSelectionDeleted(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.click(30, h.row("@jason can you"))
	gone := msg("r-platform", 5, 44, "sam", "")
	gone.Deleted = true
	f.stored["r-platform"][4] = gone
	h.send(resyncMsg{})
	h.key("c")
	if h.clips != nil {
		t.Errorf("clips = %q, want none", h.clips)
	}
	mustNotContain(t, h.view(), "copied")
}

func TestCopyDuringQuestionAnswersNo(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.key("up")
	h.key("up")
	h.key("d")
	mustContain(t, h.view(), "delete this message?")
	h.key("c")
	h.settle()
	if h.clips != nil {
		t.Errorf("clips = %q, want none", h.clips)
	}
	if got := f.writes("delete "); got != nil {
		t.Errorf("deletes = %q", got)
	}
	if h.m.note != nil && h.m.note.ask != nil {
		t.Errorf("question still shown: %q", h.m.note.text)
	}
}

func TestFooterOffersCopyOnASelection(t *testing.T) {
	h := opened(t, fixture(), 120, 20)
	h.click(30, h.row("great. one row"))
	mustContain(t, h.view(), "c copy")
	h.click(30, h.row("on it"))
	mustContain(t, h.view(), "c copy", "e edit")
}
