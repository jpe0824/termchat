package tui

import (
	"charm.land/lipgloss/v2"
)

// styles holds every style the interface draws with. Colours are the base
// ANSI 16 only; everything that matters is also carried by an attribute
// (reverse, bold) or a glyph, so the screen reads the same without colour.
type styles struct {
	selected, open, heading, faint, unread, agent, failed lipgloss.Style
	good, warn, bad                                       lipgloss.Style
}

func newStyles(color bool) styles {
	s := styles{
		selected: lipgloss.NewStyle().Reverse(true),
		open:     lipgloss.NewStyle().Bold(true),
		heading:  lipgloss.NewStyle().Bold(true),
		faint:    lipgloss.NewStyle().Faint(true),
		unread:   lipgloss.NewStyle().Bold(true),
		agent:    lipgloss.NewStyle(),
		failed:   lipgloss.NewStyle().Bold(true),
		good:     lipgloss.NewStyle(),
		warn:     lipgloss.NewStyle(),
		bad:      lipgloss.NewStyle().Bold(true),
	}
	if !color {
		return s
	}
	s.unread = s.unread.Foreground(lipgloss.Cyan)
	s.agent = s.agent.Foreground(lipgloss.Magenta)
	s.failed = s.failed.Foreground(lipgloss.Red)
	s.good = s.good.Foreground(lipgloss.Green)
	s.warn = s.warn.Foreground(lipgloss.Yellow)
	s.bad = s.bad.Foreground(lipgloss.Red)
	return s
}
