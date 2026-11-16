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

var slashCmds []slashCmd

// The table is filled in init because its commands read it for their own
// usage line.
func init() {
	slashCmds = []slashCmd{
		{"settings", "", "view and change settings", (*Model).slashSettings},
		{"notify", "all|mentions|muted", "set this room's notifications", (*Model).slashNotify},
		{"help", "", "keys and commands", (*Model).slashHelp},
	}
}

// slashUsage is how the command name is written with its arguments.
func slashUsage(name string) string {
	for _, s := range slashCmds {
		if s.name == name && s.args != "" {
			return "/" + s.name + " " + s.args
		}
	}
	return "/" + name
}

// slashParse splits a command draft into the command's name and the rest.
func slashParse(text string) (name, arg string) {
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return "", ""
	}
	return fields[0], strings.Join(fields[1:], " ")
}

// commandEnter acts on enter in a draft in command mode. A bare / does
// nothing. An enter that came within the paste gap of the key before it
// may end pasted text, so it runs nothing and asks for a deliberate one.
// With the command list showing, a name typed in part is completed.
func (m *Model) commandEnter(conv, text string, pasted bool) tea.Cmd {
	switch {
	case text == "/":
		return nil
	case pasted && strings.HasPrefix(text, "//"):
		return m.setNotice(conv, "press enter to send", false)
	case pasted:
		name, _ := slashParse(text)
		return m.setNotice(conv, "press enter to run /"+name, false)
	case m.slashPartial(m.composeFor(conv)):
		m.completeSlash(m.composeFor(conv))
		return nil
	}
	return m.runSlash(conv, text)
}

// slashDoneMsg is a command's result. said is the notice it leaves: what
// changed, or why it failed. A result whose seq is not the latest call
// from conv's draft is dropped.
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
		c.cmd, c.hide, c.pick = true, false, 0
	}
	return nil
}

// syncCmd ends command mode once the draft no longer starts with /. While
// an edit is open the mode belongs to the draft it set aside.
func (c *compose) syncCmd() {
	draft := c.in.text
	if c.edit != nil {
		draft = c.edit.saved.text
	}
	if !strings.HasPrefix(draft, "/") {
		c.cmd, c.hide = false, false
	}
}

// endCommand empties a draft whose command has run.
func (c *compose) endCommand() {
	c.in.set("")
	c.cmd, c.hide, c.pick = false, false, 0
}

// runSlash runs the command in text, a draft in command mode, on conv.
// Text that names no command stays in the draft with a notice.
func (m *Model) runSlash(conv, text string) tea.Cmd {
	name, arg := slashParse(text)
	i := slices.IndexFunc(slashCmds, func(s slashCmd) bool { return s.name == name })
	if i < 0 {
		names := make([]string, len(slashCmds))
		for j, s := range slashCmds {
			names[j] = "/" + s.name
		}
		return m.setNotice(conv, "unknown command /"+name+" — commands: "+strings.Join(names, " ")+
			"; // sends a leading /", false)
	}
	return slashCmds[i].run(m, arg)
}

// startCall tags a command that calls the background process as conv's
// latest and returns its tag; a result from an earlier one is dropped.
func (m *Model) startCall(conv string) int {
	c := m.composeFor(conv)
	c.cmdSeq++
	c.cmdText = c.in.text
	return c.cmdSeq
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
		return m.setNotice(conv, slashUsage("notify"), false)
	}
	seq, b, ref := m.startCall(conv), m.b, m.convRef(cv.Conversation)
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

// slashSettings opens the settings overlay; the command stays in the
// draft if it cannot open.
func (m *Model) slashSettings(string) tea.Cmd {
	cmd := m.openOverlay(ovSettings)
	if m.ov != nil {
		m.composeFor(m.openID).endCommand()
	}
	return cmd
}

// slashHelp opens the help overlay.
func (m *Model) slashHelp(string) tea.Cmd {
	m.composeFor(m.openID).endCommand()
	return m.keyHelp(keyCtx{})
}
