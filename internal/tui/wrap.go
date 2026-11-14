package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Widths are terminal cells, measured per grapheme cluster with the same
// method as lipgloss.Width, so a wide character or an emoji sequence counts
// as the cells it occupies.

// graphemes calls fn with each grapheme cluster of s and its width.
func graphemes(s string, fn func(g string, w int) bool) {
	for s != "" {
		g, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if g == "" {
			return
		}
		if !fn(g, w) {
			return
		}
		s = s[len(g):]
	}
}

// wrap breaks plain text into lines of at most width cells: at spaces where
// it can, inside a word where the word alone is too long. Line breaks in s
// are kept. A character wider than the whole width becomes "?".
func wrap(s string, width int) []string {
	width = max(width, 1)
	s = strings.ReplaceAll(s, "\t", "    ")
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, wrapLine(para, width)...)
	}
	return out
}

func wrapLine(p string, width int) []string {
	var lines []string
	var cur strings.Builder
	cw, empty := 0, true
	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		cw, empty = 0, true
	}
	for _, word := range strings.Split(p, " ") {
		ww := lipgloss.Width(word)
		switch {
		case !empty && cw+1+ww <= width:
			cur.WriteByte(' ')
			cur.WriteString(word)
			cw += 1 + ww
			continue
		case empty && ww <= width:
			cur.WriteString(word)
			cw, empty = ww, false
			continue
		}
		if !empty {
			flush()
		}
		if ww <= width {
			cur.WriteString(word)
			cw, empty = ww, false
			continue
		}
		graphemes(word, func(g string, gw int) bool {
			if gw > width {
				g, gw = "?", 1
			}
			if cw+gw > width {
				flush()
			}
			cur.WriteString(g)
			cw += gw
			return true
		})
		empty = false
	}
	flush()
	return lines
}

// truncate cuts s to at most width cells, ending in "…" when cut.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	graphemes(s, func(g string, w int) bool {
		if used+w > width-1 {
			return false
		}
		b.WriteString(g)
		used += w
		return true
	})
	return b.String() + "…"
}

// fit truncates s to width cells and pads it with spaces to exactly width.
// s may carry styling; only its visible cells are counted.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w > width {
		s = ansi.Truncate(s, width, "")
		w = lipgloss.Width(s)
	}
	return s + strings.Repeat(" ", width-w)
}

// spread puts left and right on one line of width cells with the gap
// between them filled with fill; right is dropped first when both do not fit.
func spread(left, right string, width int, fill string) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if right != "" && lw+rw+1 > width {
		right, rw = "", 0
	}
	if lw > width {
		return fit(left, width)
	}
	return left + strings.Repeat(fill, width-lw-rw) + right
}
