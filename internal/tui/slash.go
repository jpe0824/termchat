package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/model"
)

// configSource tells the background process a change came from the
// interface, which may change every setting.
const configSource = "tui"

// slashCmd is a command typed in the input after a lone /. run acts on the
// open conversation with the rest of the line as arg.
type slashCmd struct {
	name, args, help string
	run              func(m *Model, arg string) tea.Cmd
}

var slashCmds = []slashCmd{
	{"notify", "all|mentions|muted", "set this room's notifications", (*Model).slashNotify},
	{"help", "", "keys and commands", (*Model).slashHelp},
}

// slashDoneMsg is a command's result. said is the notice it leaves: what
// changed, or why it failed. A result whose seq is not the draft's latest
// is dropped.
type slashDoneMsg struct {
	conv string
	seq  int
	err  error
	said string
}

// slashHeld puts the open draft in command mode if it is still exactly
// the / that was typed alone.
func (m *Model) slashHeld() tea.Cmd {
	if c := m.drafts[m.openID]; c != nil && c.edit == nil && c.in.text == "/" && m.inputActive(m.geom()) {
		c.cmd = true
	}
	return nil
}

// syncCmd ends command mode once the draft no longer starts with /. The
// draft set aside by an edit keeps its mode.
func (c *compose) syncCmd() {
	if c.edit == nil && !strings.HasPrefix(c.in.text, "/") {
		c.cmd = false
	}
}

// endCommand empties a draft whose command has run.
func (c *compose) endCommand() {
	c.in.set("")
	c.cmd = false
}

// runSlash runs the command in text, a draft in command mode, on conv.
// Text that names no command stays in the draft with a notice.
func (m *Model) runSlash(conv, text string) tea.Cmd {
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	name, arg := "", ""
	if len(fields) > 0 {
		name, arg = fields[0], strings.Join(fields[1:], " ")
	}
	i := slices.IndexFunc(slashCmds, func(s slashCmd) bool { return s.name == name })
	if i < 0 {
		names := make([]string, len(slashCmds))
		for j, s := range slashCmds {
			names[j] = "/" + s.name
		}
		return m.setNotice(conv, "unknown command /"+name+" — commands: "+strings.Join(names, " ")+
			"; // sends a leading /", false)
	}
	c := m.composeFor(conv)
	c.cmdSeq++
	c.cmdText = c.in.text
	return slashCmds[i].run(m, arg)
}

// onSlashDone takes a command's result. Success empties the draft only if
// it still holds the command that ran.
func (m *Model) onSlashDone(msg slashDoneMsg) tea.Cmd {
	c := m.drafts[msg.conv]
	if c == nil || msg.seq != c.cmdSeq {
		return nil
	}
	if msg.err != nil {
		return m.setNotice(msg.conv, msg.said, false)
	}
	if c.cmd && c.edit == nil && c.in.text == c.cmdText {
		c.endCommand()
	}
	return m.setInfo(msg.conv, msg.said)
}

// slashNotify sets the open room's notification mode.
func (m *Model) slashNotify(arg string) tea.Cmd {
	conv := m.openID
	cv, ok := m.openConv()
	if !ok || cv.Kind != model.ConvRoom {
		return m.setNotice(conv, "notification modes apply to rooms only", false)
	}
	key := config.RoomPrefix + conv
	if spec, _ := config.SpecFor(key); !slices.Contains(spec.Choices, arg) {
		return m.setNotice(conv, "/notify all|mentions|muted", false)
	}
	seq, b, ref := m.composeFor(conv).cmdSeq, m.b, m.convRef(cv.Conversation)
	return func() tea.Msg {
		s, err := b.ConfigSet(key, arg, configSource)
		if err != nil {
			return slashDoneMsg{conv: conv, seq: seq, err: err, said: failure("notify", err)}
		}
		if s.Room != "" {
			ref = "#" + s.Room
		}
		return slashDoneMsg{conv: conv, seq: seq, said: ref + " notifies: " + s.Value}
	}
}

// slashHelp opens the help overlay.
func (m *Model) slashHelp(string) tea.Cmd {
	m.composeFor(m.openID).endCommand()
	return m.keyHelp(keyCtx{})
}
