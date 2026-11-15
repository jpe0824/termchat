package tui

import (
	"fmt"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/sanitize"
)

// copyMsg sends the text of it to the system clipboard through the terminal
// (OSC 52). The text is sanitised because it came from another person and is
// pasted wherever the user goes next. A terminal never answers OSC 52, so
// the notice cannot tell a refused copy from a done one.
func (m *Model) copyMsg(it item) tea.Cmd {
	text := sanitize.Text(it.m.Text)
	if text == "" {
		return nil
	}
	note := fmt.Sprintf("copied to clipboard (%d characters)", utf8.RuneCountInString(text))
	return tea.Batch(tea.SetClipboard(text), m.setInfo(m.openID, note))
}

func (m *Model) keyCopy(c keyCtx) tea.Cmd {
	if !c.hasSel {
		return nil
	}
	return m.copyMsg(c.sel)
}
