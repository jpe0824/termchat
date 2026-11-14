package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// timing is what the interface remembers about when keys arrived, to tell
// a burst of pasted keys from deliberate presses. Within one paste gap of
// each other, keys are taken as one paste; with the gap off (negative)
// every key counts as deliberate.
type timing struct {
	// lastTyped is when a key last typed into the input; lastKey is when
	// any key or paste last arrived.
	lastTyped, lastKey time.Time
	// exitKind is a key waiting out the paste gap before it acts; exitKey
	// and exitAsk hold what it will do.
	exitKind exitKind
	exitGen  int
	exitConv string
	exitKey  tea.KeyPressMsg
	exitAsk  func() tea.Cmd
	// quitAsked is when the quit warning was shown, for the key quitKey.
	quitAsked time.Time
	quitKey   string
	// askedAt is when the y/n question shown was asked.
	askedAt time.Time
	// swallow drops keys arriving in the same burst as one that found the
	// input focused with nothing to type into.
	swallow bool
	// banner is a warning drawn over the window's last row, so it shows in
	// every layout and view.
	banner    string
	bannerGen int
}

// guarded are the command keys that change something. Outside the input
// they act only as a lone press: never within the paste gap of the key
// before, and only once the gap passes with no key after. Movement keys
// stay immediate so held-down auto-repeat works.
var guarded = map[string]bool{"d": true, "e": true, "x": true, "r": true, "q": true, "i": true,
	"enter": true, "tab": true}

// stamp records a key's arrival and reports whether it came within the
// paste gap of the one before.
func (m *Model) stamp() bool {
	now, gap := m.opts.Now(), m.opts.PasteGap
	recent := gap > 0 && !m.tm.lastKey.IsZero() && now.Sub(m.tm.lastKey) < gap
	m.tm.lastKey = now
	return recent
}

// inBurst reports whether a key arrives as part of a burst of input, as an
// unbracketed paste does: an enter is waiting, or the last key typed into
// the input came less than the paste gap ago.
func (m *Model) inBurst() bool {
	if m.enterConv != "" {
		return true
	}
	gap := m.opts.PasteGap
	return gap > 0 && !m.tm.lastTyped.IsZero() && m.opts.Now().Sub(m.tm.lastTyped) < gap
}

// guardKey runs a command key, holding a guarded one to a lone press.
func (m *Model) guardKey(k tea.KeyPressMsg, g geom, recent bool) tea.Cmd {
	if m.opts.PasteGap < 0 || !guarded[k.String()] {
		return m.commandKey(k, g)
	}
	if recent {
		return nil
	}
	m.tm.exitKey = k
	return m.deferExit(exitGuarded)
}

// answer takes a key while a y/n question is shown. Any key ends the
// question; y confirms only as a lone press that did not follow the
// question within the paste gap, and anything else is the safe answer, no.
func (m *Model) answer(n *notice, s string, recent bool) tea.Cmd {
	if s != "y" {
		return nil
	}
	gap := m.opts.PasteGap
	if gap < 0 {
		return n.ask()
	}
	if recent || m.opts.Now().Sub(m.tm.askedAt) < gap {
		return nil
	}
	m.tm.exitAsk = n.ask
	return m.deferExit(exitConfirm)
}

// onPaste takes a bracketed paste. Only the focused input takes one: a
// paste anywhere else is never read as keys.
func (m *Model) onPaste(s string) tea.Cmd {
	m.tm.lastKey = m.opts.Now()
	m.cancelExit()
	m.clearBanner()
	m.flushEnter()
	m.noticeKey("", true)
	g := m.geom()
	switch {
	case m.inputActive(g):
		m.promote()
		m.composeFor(m.openID).in.insert(cleanInput(s))
		return nil
	case m.focus == focusInput && m.threadShown(g):
		return m.setNotice(m.openID, downNote, false)
	}
	return m.setBanner("paste ignored — press i to type")
}

// setBanner shows text over the last row until a key or noticeFor passes.
func (m *Model) setBanner(text string) tea.Cmd {
	m.tm.bannerGen++
	m.tm.banner = sanitize.Line(text)
	return m.opts.Tick(noticeFor, bannerExpiredMsg{gen: m.tm.bannerGen})
}

func (m *Model) clearBanner() { m.tm.banner = "" }

func (m *Model) onBannerExpired(msg bannerExpiredMsg) {
	if msg.gen == m.tm.bannerGen {
		m.tm.banner = ""
	}
}

// withBanner draws the banner over the frame's last rows: in the footer's
// place when it fits there, else wrapped over as many rows as it needs, so
// it shows in every layout and view, however narrow or short.
func (m *Model) withBanner(content string) string {
	if m.tm.banner == "" || m.w <= 0 {
		return content
	}
	rows := strings.Split(content, "\n")
	last := len(rows) - 1
	b := m.tm.banner
	if strings.HasPrefix(rows[last], "└") && lipgloss.Width(b)+5 <= m.w {
		rows[last] = spread("└─ "+m.st.bad.Render(b)+" ", "┘", m.w, "─")
		return strings.Join(rows, "\n")
	}
	lines := wrap(b, m.w)
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
			name := "@" + sanitize.Line(cv.Peer(m.me()))
			if cv.Kind == model.ConvRoom {
				name = "#" + sanitize.Line(cv.Name)
			}
			return "unsent draft in " + name + " — " + key + " again to quit"
		}
	}
	return key + " again to quit — unsent drafts will be lost"
}

// unsent reports whether c holds text or an edit worth keeping.
func (c *compose) unsent() bool {
	return c != nil && (strings.TrimSpace(c.in.text) != "" || c.edit != nil)
}
