package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// thread is the open conversation: stored messages in seq order, then the
// user's unsent items, and where the view is scrolled to.
type thread struct {
	conv   string
	stored []ipc.Message
	outbox []ipc.Message
	loaded bool
	err    string

	// atBottom follows the newest message. Otherwise the view is pinned to
	// a row: anchor is that row's key and anchorOff the line within it, so
	// the same text stays on top across new messages, older pages and
	// resizes.
	atBottom  bool
	anchor    string
	anchorOff int
	newBelow  int
	// exhausted is the oldest seq at which a read of older history came
	// back empty.
	exhausted int64
}

func newThread(conv string) *thread { return &thread{conv: conv, atBottom: true} }

// merge takes a re-read of the newest limit messages. Held messages older
// than the re-read stay, unless the re-read reached the start.
func (t *thread) merge(msgs []ipc.Message, limit int) {
	var stored, outbox []ipc.Message
	for _, m := range msgs {
		if m.Status == "" {
			stored = append(stored, m)
		} else {
			outbox = append(outbox, m)
		}
	}
	newest := t.newestSeq()
	if len(stored) >= limit && len(stored) > 0 {
		var keep []ipc.Message
		for _, m := range t.stored {
			if m.Seq < stored[0].Seq {
				keep = append(keep, m)
			}
		}
		stored = append(keep, stored...)
	}
	if t.loaded && !t.atBottom {
		for _, m := range stored {
			if m.Seq > newest {
				t.newBelow++
			}
		}
	}
	t.stored, t.outbox, t.loaded, t.err = stored, outbox, true, ""
}

// prepend adds a page of history read before seq before. The view stays on
// the row it showed when the read was asked for.
func (t *thread) prepend(before int64, msgs []ipc.Message) {
	if len(t.stored) == 0 || t.stored[0].Seq != before {
		return
	}
	var older []ipc.Message
	for _, m := range msgs {
		if m.Status == "" && m.Seq < before {
			older = append(older, m)
		}
	}
	if len(older) == 0 {
		t.exhausted = before
		return
	}
	t.stored = append(older, t.stored...)
	t.atBottom = false
}

func (t *thread) hasOlder() bool {
	return len(t.stored) > 0 && t.stored[0].Seq > 1 && t.stored[0].Seq != t.exhausted
}

func (t *thread) newestSeq() int64 {
	if n := len(t.stored); n > 0 {
		return t.stored[n-1].Seq
	}
	return 0
}

// tline is one drawn line of the thread and the row it belongs to. Rows are
// keyed by seq for stored messages and by position for unsent items, which
// may share a message id with a stored message.
type tline struct {
	key  string
	text string
}

type item struct {
	key string
	m   ipc.Message
}

func (t *thread) items() []item {
	out := make([]item, 0, len(t.stored)+len(t.outbox))
	for _, m := range t.stored {
		out = append(out, item{"s" + strconv.FormatInt(m.Seq, 10), m})
	}
	for i, m := range t.outbox {
		out = append(out, item{fmt.Sprintf("o%d:%s", i, m.MsgID), m})
	}
	return out
}

func (m *Model) author(msg ipc.Message) string {
	if msg.Author == m.me() && msg.Author != "" {
		return "you"
	}
	if n := sanitize.Line(msg.AuthorName); n != "" {
		return n
	}
	return sanitize.Line(msg.Author)
}

// body is a message's text as shown, with its markers.
func (m *Model) body(msg ipc.Message) string {
	if msg.Deleted {
		return "message deleted"
	}
	text := sanitize.Text(msg.Text)
	if msg.Agent {
		text = "[agent] " + text
	}
	if msg.Edited {
		text += " (edited)"
	}
	switch msg.Status {
	case ipc.MsgSending:
		text += "  sending…"
	case ipc.MsgFailed:
		text += "  failed — retry?"
	}
	return text
}

func (m *Model) bodyStyle(msg ipc.Message) lipgloss.Style {
	switch {
	case msg.Deleted, msg.Status == ipc.MsgSending:
		return m.st.faint
	case msg.Status == ipc.MsgFailed:
		return m.st.failed
	case msg.Agent:
		return m.st.agent
	}
	return lipgloss.NewStyle()
}

func (m *Model) day(msg ipc.Message) string {
	return msg.At.In(m.opts.Location).Format("Mon 2 Jan 2006")
}

func (m *Model) daySep(msg ipc.Message, width int) string {
	return m.st.faint.Render(spread("── "+m.day(msg)+" ", "", width, "─"))
}

// threadLines lays the thread out for a pane width cells wide.
func (m *Model) threadLines(width int, mode Mode) []tline {
	if m.thread == nil {
		return nil
	}
	items := m.thread.items()
	if mode == Tiny {
		return m.tinyLines(items, width)
	}
	authorW, gap, capW := 0, "  ", 12
	if mode == Narrow {
		gap, capW = " ", 8
	}
	for _, it := range items {
		authorW = max(authorW, min(lipgloss.Width(m.author(it.m)), capW))
	}
	prefix := 5 + len(gap) + authorW + 2
	textW := max(width-prefix, 1)
	var out []tline
	for i, it := range items {
		if i > 0 && m.day(it.m) != m.day(items[i-1].m) {
			out = append(out, tline{it.key, m.daySep(it.m, width)})
		}
		st := m.bodyStyle(it.m)
		for j, l := range wrap(m.body(it.m), textW) {
			lead := strings.Repeat(" ", prefix)
			if j == 0 {
				lead = m.st.faint.Render(it.m.At.In(m.opts.Location).Format("15:04")) + gap +
					fit(truncate(m.author(it.m), authorW), authorW) + "  "
			}
			out = append(out, tline{it.key, lead + st.Render(l)})
		}
	}
	return out
}

// tinyLines puts each author on their own line with the text indented one
// space beneath; consecutive messages by one author share the author line.
func (m *Model) tinyLines(items []item, width int) []tline {
	var out []tline
	for i, it := range items {
		newDay := i > 0 && m.day(it.m) != m.day(items[i-1].m)
		if newDay {
			out = append(out, tline{it.key, m.daySep(it.m, width)})
		}
		if i == 0 || newDay || items[i-1].m.Author != it.m.Author {
			out = append(out, tline{it.key, m.st.heading.Render(truncate(m.author(it.m), width))})
		}
		st := m.bodyStyle(it.m)
		for _, l := range wrap(m.body(it.m), width-1) {
			out = append(out, tline{it.key, " " + st.Render(l)})
		}
	}
	return out
}

// top is the index of the first visible line.
func (t *thread) top(lines []tline, vh int) int {
	maxTop := max(len(lines)-vh, 0)
	if t.atBottom {
		return maxTop
	}
	for i, l := range lines {
		if l.key == t.anchor {
			n := 0
			for i+n < len(lines) && lines[i+n].key == t.anchor {
				n++
			}
			return min(i+min(t.anchorOff, n-1), maxTop)
		}
	}
	return maxTop
}

func (t *thread) setTop(lines []tline, top, vh int) {
	maxTop := max(len(lines)-vh, 0)
	top = min(max(top, 0), maxTop)
	t.atBottom = top == maxTop
	if t.atBottom {
		t.newBelow = 0
	}
	if top < len(lines) {
		t.anchor, t.anchorOff = lines[top].key, 0
		for i := top - 1; i >= 0 && lines[i].key == t.anchor; i-- {
			t.anchorOff++
		}
	}
}

// threadPane is the geometry of the thread's message area.
func (m *Model) threadPane(g geom) (width, vh int) {
	vh = g.bodyH - 4
	if g.mode == Tiny {
		vh = g.bodyH - 3
	}
	return g.thr.inner(), max(vh, 0)
}

// scroll moves the thread view by delta lines (negative is up). Reaching the
// top asks for the page before.
func (m *Model) scroll(delta int) tea.Cmd {
	return m.scrollWith(func(top, maxTop int) int {
		// Compared, not added, so no delta can overflow.
		switch {
		case delta >= maxTop-top:
			return maxTop
		case delta <= -top:
			return 0
		}
		return top + delta
	}, delta < 0)
}

// scrollToTop shows the oldest held message and asks for the page before.
func (m *Model) scrollToTop() tea.Cmd {
	return m.scrollWith(func(int, int) int { return 0 }, true)
}

// scrollToBottom follows the newest message.
func (m *Model) scrollToBottom() tea.Cmd {
	return m.scrollWith(func(_, maxTop int) int { return maxTop }, false)
}

// scrollWith moves the view to to(top, maxTop); up says the move was
// towards older messages, so arriving at the top loads the page before.
func (m *Model) scrollWith(to func(top, maxTop int) int, up bool) tea.Cmd {
	g := m.geom()
	if m.thread == nil || g.thr.w == 0 || m.help {
		return nil
	}
	width, vh := m.threadPane(g)
	lines := m.threadLines(width, g.mode)
	maxTop := max(len(lines)-vh, 0)
	top := to(m.thread.top(lines, vh), maxTop)
	m.thread.setTop(lines, top, vh)
	if top == 0 && up {
		return m.requestOlder()
	}
	return nil
}

func (m *Model) topKey() string {
	g := m.geom()
	if m.thread == nil {
		return ""
	}
	width, vh := m.threadPane(g)
	if g.thr.w == 0 {
		width, vh = m.w-4, max(g.bodyH-4, 0)
	}
	lines := m.threadLines(width, g.mode)
	if top := m.thread.top(lines, vh); top < len(lines) {
		return lines[top].key
	}
	return ""
}
