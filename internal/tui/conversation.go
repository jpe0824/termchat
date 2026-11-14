package tui

import (
	"strconv"
	"strings"

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
	// sel is the selected row's key, "" for none.
	sel string
	// reasons holds why unsent items failed, by message id.
	reasons map[string]string
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
	t.anchor = t.remap(t.anchor)
	if t.sel = t.remap(t.sel); !t.has(t.sel) {
		t.sel = ""
	}
}

// remap follows an unsent item's key to its stored message once it has
// been delivered and is no longer unsent.
func (t *thread) remap(key string) string {
	id, ok := strings.CutPrefix(key, "o:")
	if !ok {
		return key
	}
	for _, m := range t.outbox {
		if m.MsgID == id {
			return key
		}
	}
	for _, m := range t.stored {
		if m.MsgID == id {
			return storedKey(m)
		}
	}
	return key
}

func (t *thread) has(key string) bool {
	for _, it := range t.items() {
		if it.key == key {
			return true
		}
	}
	return false
}

func (t *thread) hasFailed() bool {
	for _, m := range t.outbox {
		if m.Status == ipc.MsgFailed {
			return true
		}
	}
	return false
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
// keyed by seq for stored messages and by message id for unsent items,
// which may share a message id with a stored message. sep marks a day
// separator, which belongs to the row after it.
type tline struct {
	key  string
	text string
	sep  bool
}

type item struct {
	key string
	m   ipc.Message
}

func (t *thread) items() []item {
	out := make([]item, 0, len(t.stored)+len(t.outbox))
	for _, m := range t.stored {
		out = append(out, item{storedKey(m), m})
	}
	for _, m := range t.outbox {
		out = append(out, item{"o:" + m.MsgID, m})
	}
	return out
}

func storedKey(m ipc.Message) string { return "s" + strconv.FormatInt(m.Seq, 10) }

func (m *Model) author(msg ipc.Message) string {
	if msg.Author == m.me() && msg.Author != "" {
		return "you"
	}
	if n := sanitize.Line(msg.AuthorName); n != "" {
		return n
	}
	return sanitize.Line(msg.Author)
}

// body is a message's text as shown, with its markers. A failed item names
// its reason, and offers its keys when selected.
func (m *Model) body(msg ipc.Message, reason string, selected bool) string {
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
		text += "  failed"
		if r := sanitize.Line(reason); r != "" {
			text += " (" + r + ")"
		}
		if selected {
			text += " — r retry, x discard"
		} else {
			text += " — retry?"
		}
	}
	return text
}

// itemBody is an item's text and style, marked when selected.
func (m *Model) itemBody(it item) (string, lipgloss.Style, bool) {
	sel := it.key == m.thread.sel
	st := m.bodyStyle(it.m)
	if sel {
		st = st.Reverse(true)
	}
	return m.body(it.m, m.thread.reasons[it.m.MsgID], sel), st, sel
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
			out = append(out, tline{it.key, m.daySep(it.m, width), true})
		}
		text, st, sel := m.itemBody(it)
		g := gap
		if sel {
			g = "›" + gap[1:]
		}
		for j, l := range wrap(text, textW) {
			lead := strings.Repeat(" ", prefix)
			if j == 0 {
				lead = m.st.faint.Render(it.m.At.In(m.opts.Location).Format("15:04")) + g +
					fit(truncate(m.author(it.m), authorW), authorW) + "  "
			}
			out = append(out, tline{it.key, lead + st.Render(l), false})
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
			out = append(out, tline{it.key, m.daySep(it.m, width), true})
		}
		if i == 0 || newDay || items[i-1].m.Author != it.m.Author {
			out = append(out, tline{it.key, m.st.heading.Render(truncate(m.author(it.m), width)), false})
		}
		text, st, sel := m.itemBody(it)
		lead := " "
		if sel {
			lead = "›"
		}
		for _, l := range wrap(text, width-1) {
			out = append(out, tline{it.key, lead + st.Render(l), false})
		}
	}
	return out
}
