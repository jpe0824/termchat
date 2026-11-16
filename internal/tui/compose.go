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
	// defaultPasteGap is how long enter waits before it sends. Input
	// arriving sooner turns it into a line break: without bracketed paste a
	// pasted line break is an enter key with the next line right behind it.
	defaultPasteGap = 25 * time.Millisecond
	// counterFrom is the draft size, in bytes, past which its size shows.
	counterFrom  = 7000
	maxInputRows = 5
	// hintMinBody and gapMinBody are the body heights from which the thread
	// pane shows the key hint row and the empty row under it.
	hintMinBody = 10
	gapMinBody  = 14
	sendSource  = "tui"
)

// compose is one conversation's input: its draft, an edit in progress with
// the draft set aside, and whether a Send or an Edit is out. cmd says the
// draft is a command; cmdSeq and cmdText tag the latest command run.
type compose struct {
	in      input
	edit    *editing
	sending bool
	saving  bool
	cmd     bool
	cmdSeq  int
	cmdText string
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

// inputUsable reports whether the open conversation's input takes text:
// it is drawn, and the background process is there to send it.
func (m *Model) inputUsable(g geom) bool {
	return m.fits() && m.threadShown(g) && !m.procDown && m.b != nil
}

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

// inputKey handles a key for the focused input. No key that pasted text
// can carry moves focus or acts as a command here: tab types a tab, and
// esc and ↑ inside a burst are dropped. A / typed alone into an empty
// draft makes it a command once the paste gap passes with no other key.
func (m *Model) inputKey(k tea.KeyPressMsg, g geom) tea.Cmd {
	c := m.composeFor(m.openID)
	defer c.syncCmd()
	in, w := &c.in, m.inputWidth(g)
	switch s := k.String(); s {
	case "enter":
		if m.opts.PasteGap < 0 {
			return m.submit()
		}
		m.enterConv = m.openID
		return m.opts.Tick(m.opts.PasteGap, m.enterT.arm(timerEnter))
	case "alt+enter", "ctrl+j":
		in.insert("\n")
	case "tab":
		in.insert("\t")
	case "esc":
		if !m.tm.burst {
			return m.press(keyDeferred, m.escHold())
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
			if !m.tm.burst {
				return m.press(keyDeferred, hold{run: m.selectLastHeld})
			}
			return nil
		}
		in.up(w)
	case "down":
		in.down(w)
	case "pgup", "pgdown":
		return m.pageKey(s, g)
	case "ctrl+k", "ctrl+n", "ctrl+o":
		// Pasted text can carry these control bytes: they open an overlay
		// only as a lone press.
		if m.tm.burst {
			return nil
		}
		return m.guardKey(k, g)
	default:
		t := keyText(k)
		if t == "/" && in.text == "" && c.edit == nil && !m.tm.burst {
			in.insert(t)
			return m.press(keyDeferred, hold{run: m.slashHeld})
		}
		if t != "" {
			in.insert(cleanInput(t))
		}
	}
	return nil
}

// escHold leaves the input, if it still has the keyboard.
func (m *Model) escHold() hold {
	return hold{run: func() tea.Cmd {
		if g := m.geom(); m.focus == focusInput && m.threadShown(g) {
			m.leaveInput(g, m.composeFor(m.openID))
		}
		return nil
	}}
}

// selectLastHeld selects the user's newest message, if the input still has
// the keyboard and nothing in it.
func (m *Model) selectLastHeld() tea.Cmd {
	if c := m.drafts[m.openID]; m.inputActive(m.geom()) && (c == nil || c.in.text == "" && c.edit == nil) {
		m.selectLast()
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

func (m *Model) onEnterTimer(msg timerMsg) tea.Cmd {
	if !m.enterT.live(msg) || m.enterConv == "" {
		return nil
	}
	conv := m.enterConv
	m.enterConv = ""
	if conv != m.openID || !m.inputActive(m.geom()) {
		return nil
	}
	return m.submit()
}

// submit acts on enter: saves an edit, runs a command, or sends the draft.
// A command draft starting // sends the rest; a draft that is not a
// command sends as it is, leading / and all. A draft is sent by one Send
// call, and only when no Send for it is out.
func (m *Model) submit() tea.Cmd {
	conv := m.openID
	c := m.composeFor(conv)
	if c.edit != nil {
		return m.saveEdit(conv, c)
	}
	text := strings.TrimRight(c.in.text, "\n")
	draft := text
	if c.cmd {
		if !strings.HasPrefix(text, "//") {
			return m.runSlash(conv, text)
		}
		text = text[1:]
	}
	if c.sending {
		return m.setInfo(conv, "still sending…")
	}
	switch model.CheckText(text) {
	case model.ErrEmptyText:
		return nil
	case model.ErrTextTooLong:
		return m.tooLong(conv, text)
	}
	c.sending = true
	m.promote()
	m.scrollToBottom()
	b := m.b
	return func() tea.Msg {
		_, err := b.Send(conv, text, sendSource)
		return sentMsg{conv: conv, text: text, draft: draft, err: err}
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
	var cmd tea.Cmd
	if rest, ok := strings.CutPrefix(in.text, msg.draft); ok {
		rest = strings.TrimLeft(rest, "\n")
		pos := max(in.pos-(len(in.text)-len(rest)), 0)
		in.set(rest)
		in.pos = pos
		c.syncCmd()
	} else {
		// The draft was changed while it was sent; it stays, and says so.
		cmd = m.setInfo(msg.conv, "sent — draft kept")
	}
	if msg.conv == m.openID {
		return tea.Batch(cmd, m.requestThread())
	}
	return cmd
}

// --- geometry and drawing ---

// tlayout is where the thread pane's parts go, in rows from its top.
type tlayout struct {
	width, vh            int
	msgY, inputY, inputH int
	note                 bool
	// bar, hints and gap are the rows under the input: a rule, the message
	// key hints and an empty row. Short windows drop gap, then bar and hints.
	bar, hints, gap bool
	hintY           int
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
	// The bar only frames the hints; without them it would cost a short
	// window a message row for nothing.
	l.hints = g.bodyH >= hintMinBody
	l.bar = l.hints
	l.gap = l.hints && g.bodyH >= gapMinBody
	for _, on := range []bool{l.bar, l.hints, l.gap} {
		if on {
			fixed++
		}
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
	l.hintY = l.inputY + l.inputH + 1
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
	if l.bar {
		out = append(out, m.st.faint.Render(strings.Repeat("─", max(l.width, 0))))
	}
	if l.hints {
		out = append(out, m.hintRow(g, l))
	}
	if l.gap {
		out = append(out, "")
	}
	return out
}

// inputCursor is where the terminal's cursor goes in frame f: in the input
// when it has focus, hidden otherwise.
func (m *Model) inputCursor(f frameLayout) *tea.Cursor {
	if m.ov != nil {
		return m.ovCursor(f.ov)
	}
	g, l := f.g, f.l
	if !m.inputActive(g) {
		return nil
	}
	r, col := 0, 0
	if c := m.drafts[m.openID]; c != nil {
		_, r, col = c.in.render(m.inputWidth(g), l.inputH)
	}
	if r < 0 {
		return nil
	}
	return tea.NewCursor(g.thr.x+1+2+col, g.bodyY+l.inputY+r)
}
