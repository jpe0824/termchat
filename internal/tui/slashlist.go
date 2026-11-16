package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// maxListRows caps the command list; more matches scroll within it.
const maxListRows = 5

// slashMatches are the rows of the command list for draft c: the commands
// whose name starts with the word typed after /, while that word is still
// being typed. None while the draft is not a command, is set aside by an
// edit, or esc has hidden the list.
func (m *Model) slashMatches(c *compose) []slashCmd {
	if c == nil || !c.cmd || c.hide || c.edit != nil {
		return nil
	}
	word, ok := strings.CutPrefix(c.in.text, "/")
	if !ok || strings.ContainsAny(word, " \t\n") {
		return nil
	}
	var out []slashCmd
	for _, s := range slashCmds {
		if strings.HasPrefix(s.name, word) {
			out = append(out, s)
		}
	}
	return out
}

// shownMatches are the command list's rows as drawn: only while the input
// has the keyboard.
func (m *Model) shownMatches(g geom) []slashCmd {
	if !m.inputActive(g) {
		return nil
	}
	return m.slashMatches(m.drafts[m.openID])
}

// pickIn is c's highlighted row among n rows.
func (c *compose) pickIn(n int) int { return max(min(c.pick, n-1), 0) }

// cmdListKey takes a key the command list binds while it shows, by the
// table's "Command list" group, and reports whether it did. Inside a burst
// a guarded or deferred key is left to the input, which types a tab and
// drops esc as it does without the list.
func (m *Model) cmdListKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if len(m.slashMatches(m.drafts[m.openID])) == 0 {
		return nil, false
	}
	b, ok := groupBinding(mainCmdList, k.String())
	if !ok || b.kind != keyImmediate && m.tm.burst {
		return nil, false
	}
	key := k.String()
	return m.press(b.kind, hold{run: func() tea.Cmd {
		if len(m.shownMatches(m.geom())) == 0 {
			return nil
		}
		return b.run(m, keyCtx{key: key})
	}}), true
}

// afterInputKey keeps a draft's command state in step with a key that
// changed it from before: command mode ends without the leading /, and the
// highlight goes back to the first row.
func (c *compose) afterInputKey(before string) {
	c.syncCmd()
	if c.in.text != before {
		c.pick = 0
	}
}

// cmdListMove moves the highlight a row, stopping at either end.
func (m *Model) cmdListMove(k keyCtx) tea.Cmd {
	c := m.composeFor(m.openID)
	n := len(m.slashMatches(c))
	d := 1
	if k.key == "up" {
		d = -1
	}
	c.pick = max(min(c.pickIn(n)+d, n-1), 0)
	return nil
}

// cmdListComplete puts the highlighted command's name and a space in the
// draft.
func (m *Model) cmdListComplete(keyCtx) tea.Cmd {
	m.completeSlash(m.composeFor(m.openID))
	return nil
}

func (m *Model) completeSlash(c *compose) bool {
	rows := m.slashMatches(c)
	if len(rows) == 0 {
		return false
	}
	c.in.set("/" + rows[c.pickIn(len(rows))].name + " ")
	c.pick = 0
	return true
}

// cmdListHide hides the list; the draft keeps its text and stays a command.
func (m *Model) cmdListHide(keyCtx) tea.Cmd {
	m.composeFor(m.openID).hide = true
	return nil
}

// slashPartial reports whether enter on draft c should complete the
// highlighted command rather than run: the list shows and the typed word
// names no command exactly.
func (m *Model) slashPartial(c *compose) bool {
	word := strings.TrimPrefix(c.in.text, "/")
	for _, s := range m.slashMatches(c) {
		if s.name == word {
			return false
		}
	}
	return len(m.slashMatches(c)) > 0
}

// cmdListHeight is the rows the command list takes, at most avail.
func (m *Model) cmdListHeight(g geom, avail int) int {
	return min(len(m.shownMatches(g)), maxListRows, max(avail, 0))
}

// cmdListLines draws the command list's rows: "/name args  help", the
// highlighted one marked as overlay rows are and the typed word marked.
func (m *Model) cmdListLines(g geom, l tlayout) []string {
	rows := m.shownMatches(g)
	if l.listH == 0 || len(rows) == 0 {
		return nil
	}
	c := m.drafts[m.openID]
	pick := c.pickIn(len(rows))
	typed := len(strings.TrimRight(c.in.text, "\n"))
	top := window(0, pick, len(rows), l.listH)
	out := make([]string, 0, l.listH)
	for i := top; i < min(top+l.listH, len(rows)); i++ {
		s := rows[i]
		text := "/" + s.name
		if s.args != "" {
			text += " " + s.args
		}
		if s.help != "" {
			text += "  " + s.help
		}
		out = append(out, m.rowLine(ovRow{text: text, hlAt: 0, hlEnd: typed}, i == pick, l.width))
	}
	return out
}

// cmdListFoot is the command list's key hints, from the table.
func cmdListFoot() []string {
	var out []string
	for _, g := range keyTable {
		if g.main != mainCmdList {
			continue
		}
		for _, b := range g.binds {
			if b.foot != "" {
				out = append(out, b.foot)
			}
		}
	}
	return out
}
