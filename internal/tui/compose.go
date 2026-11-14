package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/model"
)

const (
	// enterGap is how long enter waits before it sends. Input arriving
	// sooner turns it into a line break: without bracketed paste a pasted
	// line break is an enter key with the next line right behind it.
	enterGap = 25 * time.Millisecond
	// counterFrom is the draft size, in bytes, past which its size shows.
	counterFrom  = 7000
	maxInputRows = 5
	sendSource   = "tui"
)

// compose is one conversation's input: its draft, an edit in progress with
// the draft set aside, and whether a Send or an Edit is out.
type compose struct {
	in      input
	edit    *editing
	sending bool
	saving  bool
}

type editing struct {
	msgID string
	orig  string
	saved input
}

func (m *Model) composeFor(conv string) *compose {
	c := m.drafts[conv]
	if c == nil {
		c = &compose{}
		m.drafts[conv] = c
	}
	return c
}

func (m *Model) draftText(conv string) string {
	if c := m.drafts[conv]; c != nil {
		return c.in.text
	}
	return ""
}

// threadShown reports whether the thread pane is drawn with a conversation.
func (m *Model) threadShown(g geom) bool {
	return g.thr.w > 0 && m.openID != "" && m.thread != nil && !m.help
}

// inputUsable reports whether the open conversation's input takes text.
func (m *Model) inputUsable(g geom) bool { return m.threadShown(g) && !m.procDown && m.b != nil }

func (m *Model) inputActive(g geom) bool { return m.focus == focusInput && m.inputUsable(g) }

// toInput moves focus to the input; outside an edit the selection goes.
func (m *Model) toInput() {
	m.focus = focusInput
	if c := m.drafts[m.openID]; (c == nil || c.edit == nil) && m.thread != nil {
		m.thread.sel = ""
	}
}

// toList moves focus to the chat list; narrow layouts show it.
func (m *Model) toList(g geom) {
	m.focus = focusList
	if g.mode <= Narrow {
		m.showList = true
		m.cursor = max(m.indexOf(m.openID), 0)
	}
}

// keyText is the text a key types, or "" for a command key. Shift and the
// lock keys some terminals report still type.
func keyText(k tea.KeyPressMsg) string {
	if k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModHyper|tea.ModSuper) != 0 {
		return ""
	}
	return k.Text
}

func (m *Model) inputKey(k tea.KeyPressMsg, g geom) tea.Cmd {
	c := m.composeFor(m.openID)
	in, w := &c.in, m.inputWidth(g)
	switch s := k.String(); s {
	case "enter":
		m.enterGen++
		m.enterConv = m.openID
		return m.opts.Tick(enterGap, enterMsg{gen: m.enterGen})
	case "alt+enter", "ctrl+j":
		in.insert("\n")
	case "esc":
		m.leaveInput(g, c)
	case "tab":
		if g.mode >= Standard {
			m.focus = focusList
		}
	case "left":
		in.left()
	case "right":
		in.right()
	case "home", "ctrl+a":
		in.home()
	case "end", "ctrl+e":
		in.end()
	case "backspace", "ctrl+h":
		in.backspace()
	case "delete":
		in.del()
	case "ctrl+w":
		in.killWord()
	case "ctrl+u":
		in.killLine()
	case "up":
		if in.text == "" && c.edit == nil {
			m.selectLast()
			return nil
		}
		in.up(w)
	case "down":
		in.down(w)
	case "pgup", "pgdown":
		return m.pageKey(s, g)
	default:
		if t := keyText(k); t != "" {
			in.insert(cleanInput(t))
		}
	}
	return nil
}

func (m *Model) leaveInput(g geom, c *compose) {
	switch {
	case c.edit != nil:
		m.cancelEdit(c)
	case m.thread != nil && m.thread.sel != "":
		m.focus = focusThread
	default:
		m.toList(g)
	}
}

// flushEnter turns an enter still waiting into a line break: more input
// right behind it means it was part of a paste.
func (m *Model) flushEnter() {
	if m.enterConv != "" {
		m.composeFor(m.enterConv).in.insert("\n")
		m.enterConv = ""
	}
}

func (m *Model) onEnterTimer(msg enterMsg) tea.Cmd {
	if msg.gen != m.enterGen || m.enterConv == "" {
		return nil
	}
	conv := m.enterConv
	m.enterConv = ""
	if conv != m.openID || !m.inputActive(m.geom()) {
		return nil
	}
	return m.submit()
}

func (m *Model) onPaste(s string) tea.Cmd {
	m.flushEnter()
	m.noticeKey("")
	g := m.geom()
	if !m.inputUsable(g) {
		return nil
	}
	m.toInput()
	m.composeFor(m.openID).in.insert(cleanInput(s))
	return nil
}

// submit acts on enter: saves an edit, or sends the draft. A draft is sent
// by one Send call, and only when no Send for it is out.
func (m *Model) submit() tea.Cmd {
	conv := m.openID
	c := m.composeFor(conv)
	if c.edit != nil {
		return m.saveEdit(conv, c)
	}
	if c.sending {
		return nil
	}
	text := strings.TrimRight(c.in.text, "\n")
	switch model.CheckText(text) {
	case model.ErrEmptyText:
		return nil
	case model.ErrTextTooLong:
		return m.tooLong(conv, text)
	}
	c.sending = true
	m.scrollToBottom()
	b := m.b
	return func() tea.Msg {
		_, err := b.Send(conv, text, sendSource)
		return sentMsg{conv: conv, text: text, err: err}
	}
}

func (m *Model) tooLong(conv, text string) tea.Cmd {
	return m.setNotice(conv, fmt.Sprintf("too long to send: %d/%d bytes", len(text), model.MaxTextBytes), false)
}

// onSent takes the sent text out of the draft it came from, whichever
// conversation is open now. A failed send leaves the draft as it is.
func (m *Model) onSent(msg sentMsg) tea.Cmd {
	c := m.composeFor(msg.conv)
	c.sending = false
	if msg.err != nil {
		return m.setNotice(msg.conv, failure("send", msg.err), false)
	}
	in := &c.in
	if c.edit != nil {
		in = &c.edit.saved
	}
	if rest, ok := strings.CutPrefix(in.text, msg.text); ok {
		rest = strings.TrimLeft(rest, "\n")
		pos := max(in.pos-(len(in.text)-len(rest)), 0)
		in.set(rest)
		in.pos = pos
	}
	if msg.conv == m.openID {
		return m.requestThread()
	}
	return nil
}

// --- geometry and drawing ---

// tlayout is where the thread pane's parts go, in rows from its top.
type tlayout struct {
	width, vh            int
	msgY, inputY, inputH int
	note                 bool
}

func (m *Model) tlayout(g geom) tlayout {
	l := tlayout{width: g.thr.inner(), msgY: 2}
	if g.mode == Tiny {
		l.msgY = 1
	}
	fixed := l.msgY + 1
	if s, _ := m.noteLine(); s != "" {
		l.note = true
		fixed++
	}
	n := 1
	if c := m.drafts[m.openID]; c != nil {
		n = len(c.in.rows(m.inputWidth(g)))
	}
	// The input leaves the thread at least one row when the window allows.
	l.inputH = max(min(n, maxInputRows, g.bodyH-fixed-1), 1)
	l.vh = max(g.bodyH-fixed-l.inputH, 0)
	l.inputY = l.msgY + l.vh + 1
	if l.note {
		l.inputY++
	}
	return l
}

// inputWidth is the cells the input's text is drawn in, after "> ".
func (m *Model) inputWidth(g geom) int { return max(g.thr.inner()-2, 1) }

// inputArea draws the rule above the input, the notice line and the input.
func (m *Model) inputArea(g geom, l tlayout) []string {
	c := m.drafts[m.openID]
	if c == nil {
		c = &compose{}
	}
	rule := strings.Repeat("─", max(l.width, 0))
	if n := len(c.in.text); n > counterFrom {
		count := fmt.Sprintf(" %d/%d", n, model.MaxTextBytes)
		st := m.st.faint
		if n > model.MaxTextBytes {
			st = m.st.bad
		}
		rule = m.st.faint.Render(strings.Repeat("─", max(l.width-len(count), 0))) + st.Render(count)
	} else {
		rule = m.st.faint.Render(rule)
	}
	out := []string{rule}
	if s, st := m.noteLine(); l.note {
		out = append(out, st.Render(truncate(s, l.width)))
	}
	rows, _, _ := c.in.render(m.inputWidth(g), l.inputH)
	text := lipgloss.NewStyle()
	if !m.inputUsable(g) {
		text = m.st.faint
	}
	for i := range max(len(rows), 1) {
		lead, r := "  ", ""
		if i == 0 {
			lead = "> "
		}
		if i < len(rows) {
			r = text.Render(rows[i])
		}
		out = append(out, lead+r)
	}
	return out
}

// inputCursor is where the terminal's cursor goes: in the input when it has
// focus, hidden otherwise.
func (m *Model) inputCursor() *tea.Cursor {
	if m.splashShown() || m.w < minW || m.h < minH {
		return nil
	}
	g := m.geom()
	if !m.inputActive(g) {
		return nil
	}
	l := m.tlayout(g)
	r, col := 0, 0
	if c := m.drafts[m.openID]; c != nil {
		_, r, col = c.in.render(m.inputWidth(g), l.inputH)
	}
	if r < 0 {
		return nil
	}
	return tea.NewCursor(g.thr.x+1+2+col, g.bodyY+l.inputY+r)
}
