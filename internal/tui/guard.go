package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/sanitize"
)

// timing is what the interface remembers about when keys arrived, to tell
// a burst of pasted keys from deliberate presses. Within one paste gap of
// each other, keys are taken as one paste; with the gap off (negative)
// every key counts as deliberate.
type timing struct {
	// lastKey is when any key or paste last arrived.
	lastKey time.Time
	// recent says the key being handled came within the paste gap of the
	// one before; burst says that, or that an enter is still waiting.
	recent, burst bool
	// pending is a key waiting out the paste gap before it acts.
	pending *hold
	exitT   timer
	// quitAsked is when the quit warning was shown, for the key quitKey.
	quitAsked time.Time
	quitKey   string
	// askedAt is when the y/n question shown was asked.
	askedAt time.Time
	// swallow drops keys arriving in the same burst as one that found the
	// input focused with nothing to type into.
	swallow bool
	// banner is drawn over the window's last row, so it shows in every
	// layout and view.
	banner  bannerNote
	bannerT timer
}

// bannerNote is a warning, or a hint: the note left by a key that types
// nothing where it was pressed, which stays up through the rest of that
// key's burst. short is what it says instead when the window has too few
// rows for all of text.
type bannerNote struct {
	text, short string
	hint        bool
}

// stamp records a key's arrival and reports whether it came within the
// paste gap of the one before.
func (m *Model) stamp() bool {
	now, gap := m.opts.Now(), m.opts.PasteGap
	recent := gap > 0 && !m.tm.lastKey.IsZero() && now.Sub(m.tm.lastKey) < gap
	m.tm.lastKey = now
	return recent
}

// guardKey runs a command key, holding a guarded one to a lone press. The
// hold is bound to the chat list's row or the thread's selection, so when
// it acts the keyboard is still where the binding was looked up.
func (m *Model) guardKey(k tea.KeyPressMsg, g geom) tea.Cmd {
	b, ok := mainBinding(m.listActive(g), k.String())
	if !ok || b.kind != keyGuarded {
		return m.commandKey(k, g, b, ok)
	}
	return m.press(keyGuarded, hold{run: func() tea.Cmd {
		if m.ov != nil {
			return nil
		}
		return m.commandKey(k, m.geom(), b, ok)
	}})
}

// target names what a command key acts on: the chat list's row, or the
// thread's selected message.
func (m *Model) target(g geom) string {
	switch {
	case m.ov != nil:
		return m.ovTarget()
	case m.listActive(g):
		return "row " + m.cursorID()
	case m.thread != nil:
		return "message " + m.thread.sel
	}
	return ""
}

// hintToType is what a key that would type, pressed outside the input,
// does instead of typing: it names the key that leads to the input from
// where the keyboard is. One timer serves a whole burst of them.
func (m *Model) hintToType(list bool) tea.Cmd {
	text := "press i to type"
	if list {
		text = "press enter to open a chat"
	}
	if m.b == nil || m.procDown || m.tm.banner.hint && m.tm.banner.text == text {
		return nil
	}
	return m.showBanner(bannerNote{text: text, hint: true}, noticeFor)
}

// scrollKey scrolls the thread for a movement key.
func (m *Model) scrollKey(d int) tea.Cmd { return m.held(func() tea.Cmd { return m.scroll(d) }) }

// held runs scroll, a scroll of the thread by a key. Text can carry the
// keys that scroll, so a preview they scroll is opened only once the paste
// gap passes with no key after.
func (m *Model) held(scroll func() tea.Cmd) tea.Cmd {
	was := m.preview
	cmd := scroll()
	if !was || m.preview || m.opts.PasteGap < 0 {
		return cmd
	}
	m.preview, m.readOpen = true, false
	return tea.Batch(cmd, m.wait(hold{run: func() tea.Cmd { m.promote(); return nil }}))
}

// onPaste takes a bracketed paste. Only the focused input takes one: a
// paste anywhere else is never read as keys.
func (m *Model) onPaste(s string) tea.Cmd {
	m.tm.lastKey = m.opts.Now()
	m.cancelExit()
	m.clearBanner()
	m.flushEnter()
	m.noticeKey("")
	g := m.geom()
	switch {
	case m.ov != nil && m.help:
		// Help covers the overlay: a paste goes nowhere and needs no word.
		return nil
	case m.ov != nil:
		return m.ovPaste(s)
	case m.inputActive(g):
		m.promote()
		m.composeFor(m.openID).in.insert(cleanInput(s))
		return nil
	case m.focus == focusInput && m.threadShown(g) && !m.fits():
		return nil
	case m.focus == focusInput && m.threadShown(g):
		return m.setNotice(m.openID, downNote, false)
	case m.listActive(g):
		return m.setBanner("paste ignored — press enter to open a chat")
	}
	return m.setBanner("paste ignored — press i to type")
}

// setBanner shows text over the last row until a key or noticeFor passes.
func (m *Model) setBanner(text string) tea.Cmd {
	return m.showBanner(bannerNote{text: text}, noticeFor)
}

// showBanner shows b over the last rows until a key or d passes.
func (m *Model) showBanner(b bannerNote, d time.Duration) tea.Cmd {
	b.text = sanitize.Line(b.text)
	m.tm.banner = b
	return m.opts.Tick(d, m.tm.bannerT.arm(timerBanner))
}

func (m *Model) clearBanner() { m.tm.banner = bannerNote{} }

// withBanner draws the banner over the frame's last rows: in the footer's
// place when it fits there, else wrapped over as many rows as it needs, or
// its short form when the frame has fewer rows than that, so it shows in
// every layout and view, however narrow or short.
func (m *Model) withBanner(content string) string {
	if m.tm.banner.text == "" || m.w <= 0 {
		return content
	}
	rows := strings.Split(content, "\n")
	last := len(rows) - 1
	b := m.tm.banner.text
	if strings.HasPrefix(rows[last], "└") && lipgloss.Width(b)+5 <= m.w {
		rows[last] = spread("└─ "+m.st.bad.Render(b)+" ", "┘", m.w, "─")
		return strings.Join(rows, "\n")
	}
	lines := wrap(b, m.w)
	if len(lines) > len(rows) && m.tm.banner.short != "" {
		lines = wrap(m.tm.banner.short, m.w)
	}
	n := min(len(lines), len(rows))
	for i := range n {
		rows[len(rows)-n+i] = fit(m.st.bad.Render(lines[i]), m.w)
	}
	return strings.Join(rows, "\n")
}

// quitWarning is the warning for a first quit key press with drafts kept.
func (m *Model) quitWarning(key string) string {
	if c := m.drafts[m.openID]; c.unsent() {
		return key + " again to quit — unsent drafts will be lost"
	}
	for _, cv := range m.convs {
		if m.drafts[cv.ID].unsent() {
			return "unsent draft in " + m.convRef(cv.Conversation) + " — " + key + " again to quit"
		}
	}
	return key + " again to quit — unsent drafts will be lost"
}

// unsent reports whether c holds text or an edit worth keeping.
func (c *compose) unsent() bool {
	return c != nil && (strings.TrimSpace(c.in.text) != "" || c.edit != nil)
}
