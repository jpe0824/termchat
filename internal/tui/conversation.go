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
	// flash is a row shown marked for a while: a message opened from
	// search. jump is the seq of one still to be brought into view, and
	// want how many of the newest messages the first read takes to reach it.
	flash string
	jump  int64
	want  int
	// owed says a re-read is owed, and need what it must reach: the oldest
	// message an event touched, 0 for the whole held window.
	owed bool
	need int64
	// agents are the authors of stored messages sent as agents.
	agents map[string]bool
	// rev counts changes to what the thread holds, for lines, its drawn
	// lines kept between frames.
	rev   int
	lines lineCache
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
	t.agents = map[string]bool{}
	t.addAgents(stored)
	t.rev++
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
	t.addAgents(older)
	t.atBottom = false
	t.rev++
}

func (t *thread) addAgents(msgs []ipc.Message) {
	for _, m := range msgs {
		if m.Agent {
			if t.agents == nil {
				t.agents = map[string]bool{}
			}
			t.agents[m.Author] = true
		}
	}
}

// owe records a re-read owed back to message seq, 0 for the whole window.
func (t *thread) owe(seq int64) {
	switch {
	case !t.owed:
		t.owed, t.need = true, seq
	case t.need != 0 && (seq == 0 || seq < t.need):
		t.need = seq
	}
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
