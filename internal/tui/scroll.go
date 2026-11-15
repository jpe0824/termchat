package tui

import (
	tea "charm.land/bubbletea/v2"
)

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

// threadPane is the geometry of the thread's message area: what the
// header, the input and the notice line leave.
func (m *Model) threadPane(g geom) (width, vh int) {
	l := m.tlayout(g)
	return l.width, l.vh
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
	m.promote()
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
