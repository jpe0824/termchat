package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

const (
	// searchWait is how long typing must pause before the query runs.
	searchWait  = 150 * time.Millisecond
	searchLimit = 50
	// flashFor is how long a message opened from search stays marked.
	flashFor = 2 * time.Second
	// jumpContext is how many messages newer than one opened from search
	// are read with it.
	jumpContext = 10
)

// searchChanged starts the wait for a new query; a query typed over it
// makes the earlier one stale.
func (m *Model) searchChanged() tea.Cmd {
	ov := m.ov
	tick := ov.seq.arm(timerSearch)
	tick.ov = ov.gen
	if strings.TrimSpace(ov.field.text) == "" {
		ov.results, ov.searched = nil, false
		return nil
	}
	return m.opts.Tick(searchWait, tick)
}

func (m *Model) onSearchTick(msg timerMsg) tea.Cmd {
	ov := m.ov
	if ov == nil || ov.gen != msg.ov || !ov.seq.live(msg) || m.b == nil {
		return nil
	}
	q, b := strings.TrimSpace(ov.field.text), m.b
	return func() tea.Msg {
		msgs, err := b.Search(q, searchLimit)
		return searchMsg{b: b, gen: msg.ov, seq: msg.gen, q: q, msgs: msgs, err: err}
	}
}

// onSearch shows results only for the newest query of the open overlay.
func (m *Model) onSearch(msg searchMsg) {
	ov := m.ov
	if ov == nil || ov.gen != msg.gen || ov.seq != msg.seq || m.stale(msg.b, "") {
		return
	}
	ov.searched, ov.sel, ov.top, ov.clicked = true, 0, 0, ""
	if msg.err != nil {
		ov.results, ov.err = nil, failure("search", msg.err)
		return
	}
	ov.results, ov.err = msg.msgs, ""
}

// searchRows are the results: conversation · author · text, with the
// first word of the query found in the text marked.
func (m *Model) searchRows() []ovRow {
	ov := m.ov
	words := strings.Fields(strings.TrimSpace(ov.field.text))
	rows := make([]ovRow, 0, len(ov.results))
	for _, r := range ov.results {
		conv := sanitize.Line(r.ConvID)
		if m.left(r.ConvID) {
			conv += " (left)"
		}
		if i := m.indexOf(r.ConvID); i >= 0 {
			// A room keeps the chat list's "# room".
			if c := m.convs[i].Conversation; c.Kind == model.ConvDM {
				conv = m.convRef(c)
			} else {
				conv = m.convName(c)
			}
		}
		text := sanitize.Line(r.Text)
		lead := conv + " · " + m.author(r) + " · "
		row := ovRow{id: "s:" + r.ConvID + ":" + strconv.FormatInt(r.Seq, 10), text: lead + text, from: len(lead)}
		for _, w := range words {
			if at, end := foldMatch(text, w); at >= 0 {
				row.hlAt, row.hlEnd = len(lead)+at, len(lead)+end
				break
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (m *Model) searchEmpty() string {
	switch ov := m.ov; {
	case strings.TrimSpace(ov.field.text) == "":
		return ""
	case !ov.searched:
		return "searching…"
	}
	return "no results"
}

// searchGo opens the selected result's conversation at that message.
func (m *Model) searchGo() tea.Cmd {
	i := m.ov.sel
	if i < 0 || i >= len(m.ov.results) {
		return nil
	}
	r := m.ov.results[i]
	if m.left(r.ConvID) {
		m.ov.err = "you are no longer in this conversation"
		return nil
	}
	m.closeOverlay()
	return m.openAt(r.ConvID, r.Seq)
}

// left reports whether the chat list, once read, no longer holds conv: its
// history stays searchable, but it cannot be opened.
func (m *Model) left(conv string) bool { return m.haveList && m.indexOf(conv) < 0 }

// openAt opens conv with message seq in view and marked for a while,
// reading history back to it first when it is older than what is held.
func (m *Model) openAt(conv string, seq int64) tea.Cmd {
	cmd := m.openSeek(conv, seq)
	t := m.thread
	t.jump = seq
	t.flash = "s" + strconv.FormatInt(seq, 10)
	flash := m.opts.Tick(flashFor, m.flashT.arm(timerFlash))
	if cmd == nil {
		cmd = m.seekJump()
	}
	return tea.Batch(cmd, flash)
}

// jumpWant is how many of conv's newest messages to read so that seq is
// among them.
func (m *Model) jumpWant(conv string, seq int64) int {
	if i := m.indexOf(conv); i >= 0 && m.convs[i].LastSeq > seq {
		return int(min(m.convs[i].LastSeq-seq+1+jumpContext, maxReread))
	}
	return 0
}

// seekJump brings the message the thread waits to show into view: once it
// is held, or once there is nothing older to read.
func (m *Model) seekJump() tea.Cmd {
	t := m.thread
	if t == nil || t.jump == 0 || !t.loaded {
		return nil
	}
	if len(t.stored) > 0 && t.stored[0].Seq > t.jump && t.hasOlder() {
		return m.requestOlder()
	}
	key := "s" + strconv.FormatInt(t.jump, 10)
	t.jump = 0
	if !t.has(key) {
		return nil
	}
	g := m.geom()
	width, vh := m.threadPane(g)
	lines := m.threadLines(width, g.mode)
	for i, l := range lines {
		if l.key == key {
			t.setTop(lines, max(i-1, 0), vh)
			break
		}
	}
	return nil
}
