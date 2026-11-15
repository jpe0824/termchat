package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// lineCache keeps a thread's drawn lines between frames: the lines of
// every message as drawn unselected, for the inputs in base, where each
// message's lines are among them, and each message's text as wrapped.
// Selection and flash are drawn over the kept lines on each call.
type lineCache struct {
	base  linesKey
	items []item
	lay   lineLayout
	lines []tline
	spans map[string]lineSpan
	// rows are the wrapped texts in use; prev, while the lines are laid out
	// again, the ones in use before, which those not used again leave.
	rows, prev map[wrapKey][]string
}

// lineSpan is where message i's lines are: lines[from:to].
type lineSpan struct{ from, to, i int }

// linesKey is what a thread's lines as a whole depend on, beyond each
// message: the author column's width follows from it.
type linesKey struct {
	set   bool
	rev   int
	width int
	mode  Mode
	me    string
}

// wrapKey is what a message's wrapped text depends on.
type wrapKey struct {
	key, text, status, reason  string
	edited, deleted, agent, on bool
	width                      int
}

// lineLayout is the column layout of a thread's lines.
type lineLayout struct {
	mode                         Mode
	width, textW, authorW, prefx int
	gap                          string
}

// threadLines lays the thread out for a pane width cells wide.
func (m *Model) threadLines(width int, mode Mode) []tline {
	if m.thread == nil {
		return nil
	}
	return m.layLines(width, mode, &m.thread.lines)
}

// layLines lays the thread out, keeping what it can in c; with c nil it
// keeps nothing.
func (m *Model) layLines(width int, mode Mode, c *lineCache) []tline {
	t := m.thread
	key := linesKey{true, t.rev, width, mode, m.me()}
	if c == nil {
		c = &lineCache{}
	}
	if c.base != key {
		m.layBase(c, key)
	}
	var patch []string
	for _, k := range []string{t.sel, t.flash} {
		if _, ok := c.spans[k]; ok && !slices.Contains(patch, k) {
			patch = append(patch, k)
		}
	}
	if len(patch) == 0 {
		return c.lines
	}
	slices.SortFunc(patch, func(a, b string) int { return c.spans[a].from - c.spans[b].from })
	out := make([]tline, 0, len(c.lines)+4)
	at := 0
	for _, k := range patch {
		s := c.spans[k]
		out = append(out, c.lines[at:s.from]...)
		out = append(out, m.itemLines(c, s.i, k == t.sel, k == t.flash)...)
		at = s.to
	}
	return append(out, c.lines[at:]...)
}

// layBase lays out every message unselected for key.
func (m *Model) layBase(c *lineCache, key linesKey) {
	items := m.thread.items()
	lay := lineLayout{mode: key.mode, width: key.width, gap: "  "}
	if key.mode != Tiny {
		capW := 12
		if key.mode == Narrow {
			lay.gap, capW = " ", 8
		}
		for _, it := range items {
			lay.authorW = max(lay.authorW, min(lipgloss.Width(m.author(it.m)), capW))
		}
		lay.prefx = 5 + len(lay.gap) + lay.authorW + 2
		lay.textW = max(key.width-lay.prefx, 1)
	}
	*c = lineCache{base: key, items: items, lay: lay, spans: map[string]lineSpan{}, rows: map[wrapKey][]string{},
		prev: c.rows}
	for i, it := range items {
		from := len(c.lines)
		c.lines = append(c.lines, m.itemLines(c, i, false, false)...)
		c.spans[it.key] = lineSpan{from, len(c.lines), i}
	}
	c.prev = nil
}

// itemLines are the lines of message i of c's items: a day separator
// when its day differs from the message before, in the tiny layout an
// author line when the author does, then its text.
func (m *Model) itemLines(c *lineCache, i int, sel, flash bool) []tline {
	items, lay := c.items, c.lay
	it := items[i]
	var out []tline
	newDay := i > 0 && m.day(it.m) != m.day(items[i-1].m)
	if newDay {
		out = append(out, tline{it.key, m.daySep(it.m, lay.width), true})
	}
	st := m.bodyStyle(it.m)
	if sel || flash {
		st = st.Reverse(true)
	}
	if lay.mode == Tiny {
		if i == 0 || newDay || items[i-1].m.Author != it.m.Author {
			out = append(out, tline{it.key, m.st.heading.Render(truncate(m.author(it.m), lay.width)), false})
		}
		lead := " "
		if sel {
			lead = "›"
		}
		for _, l := range m.wrapped(c, it, sel, lay.width-1) {
			out = append(out, tline{it.key, lead + st.Render(l), false})
		}
		return out
	}
	g := lay.gap
	if sel {
		g = "›" + g[1:]
	}
	for j, l := range m.wrapped(c, it, sel, lay.textW) {
		lead := strings.Repeat(" ", lay.prefx)
		if j == 0 {
			lead = m.st.faint.Render(it.m.At.In(m.opts.Location).Format("15:04")) + g +
				fit(truncate(m.author(it.m), lay.authorW), lay.authorW) + "  "
		}
		out = append(out, tline{it.key, lead + st.Render(l), false})
	}
	return out
}

// wrapped is message it's text as shown, wrapped to width cells.
func (m *Model) wrapped(c *lineCache, it item, sel bool, width int) []string {
	reason := m.thread.reasons[it.m.MsgID]
	k := wrapKey{key: it.key, text: it.m.Text, status: it.m.Status, reason: reason, edited: it.m.Edited,
		deleted: it.m.Deleted, agent: it.m.Agent, on: sel && it.m.Status == ipc.MsgFailed, width: width}
	rows, ok := c.rows[k]
	if !ok {
		if rows, ok = c.prev[k]; !ok {
			rows = wrap(m.body(it.m, reason, sel), width)
		}
		c.rows[k] = rows
	}
	return rows
}
