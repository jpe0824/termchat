package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// banner is the opening screen's title, 69 columns by 6 rows.
var banner = []string{
	"████████╗███████╗██████╗ ███╗   ███╗ ██████╗██╗  ██╗ █████╗ ████████╗",
	"╚══██╔══╝██╔════╝██╔══██╗████╗ ████║██╔════╝██║  ██║██╔══██╗╚══██╔══╝",
	"   ██║   █████╗  ██████╔╝██╔████╔██║██║     ███████║███████║   ██║",
	"   ██║   ██╔══╝  ██╔══██╗██║╚██╔╝██║██║     ██╔══██║██╔══██║   ██║",
	"   ██║   ███████╗██║  ██║██║ ╚═╝ ██║╚██████╗██║  ██║██║  ██║   ██║",
	"   ╚═╝   ╚══════╝╚═╝  ╚═╝╚═╝     ╚═╝ ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝",
}

const (
	bannerCols = 69
	bannerMinW = 72
	plainTitle = "T E R M C H A T"
	joinHint   = "termchat join <host:port> --invite <code>"
)

// splashStatus is the opening screen's connection line, or the way to join
// when no account is configured.
func (m *Model) splashStatus() []string {
	if m.noAccount {
		return []string{"no server joined yet", "", joinHint, "", m.st.faint.Render("q quit")}
	}
	if m.launching {
		return []string{m.st.warn.Render("○") + " starting…"}
	}
	glyph, words, st := m.state()
	switch {
	case m.server != "" && words == "connected":
		words = "connected to " + m.server
	case m.server != "" && words == "connecting":
		words = "connecting to " + m.server + "…"
	case words == "connecting":
		words = "connecting…"
	}
	return []string{st.Render(glyph) + " " + words}
}

// splash draws the opening screen, centred.
func (m *Model) splash() string {
	if m.w < minW || m.h < minH {
		return tooSmall[:min(len(tooSmall), max(m.w, 0))]
	}
	tail := append([]string{"", "chat for the terminal", m.opts.Version, ""}, m.splashStatus()...)
	var lines []string
	block := 0
	if m.w >= bannerMinW && m.h >= len(banner)+len(tail)+2 {
		lines, block = append(lines, banner...), len(banner)
	} else {
		lines, block = append(lines, plainTitle), 0
	}
	lines = append(lines, tail...)
	if len(lines) > m.h {
		var kept []string
		for _, l := range lines {
			if l != "" {
				kept = append(kept, l)
			}
		}
		lines = kept[:min(len(kept), m.h)]
	}
	topPad := (m.h - len(lines)) / 2
	out := make([]string, m.h)
	for i := range out {
		j := i - topPad
		if j < 0 || j >= len(lines) {
			out[i] = strings.Repeat(" ", m.w)
			continue
		}
		w := lipgloss.Width(lines[j])
		if j < block {
			w = bannerCols
		}
		out[i] = fit(strings.Repeat(" ", max((m.w-w)/2, 0))+lines[j], m.w)
	}
	return strings.Join(out, "\n")
}
