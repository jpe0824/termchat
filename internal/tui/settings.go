package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/notify"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// PasteGapFor is the PasteGap option for config paste_gap_ms: 0 or less
// turns the wait off.
func PasteGapFor(ms int) time.Duration {
	if ms <= 0 {
		return -1
	}
	return time.Duration(ms) * time.Millisecond
}

// settingGroups are the settings overlay's headings, in order.
var settingGroups = []string{"Notifications", "Interface", "CLI", "Rooms", "Account"}

// accountKey is the key of the read-only account entries.
const accountKey = "account"

func settingGroup(key string) string {
	switch {
	case strings.HasPrefix(key, "notify."):
		return "Notifications"
	case strings.HasPrefix(key, "cli."):
		return "CLI"
	case strings.HasPrefix(key, config.RoomPrefix):
		return "Rooms"
	case key == accountKey:
		return "Account"
	}
	return "Interface"
}

// labelWidth is the cells a setting's label takes before its value.
const labelWidth = 17

// settingLabel is how a setting is named: its key, or #room for a room's
// notification mode.
func settingLabel(s ipc.Setting) string {
	if strings.HasPrefix(s.Key, config.RoomPrefix) && s.Room != "" {
		return "#" + sanitize.Line(s.Room)
	}
	return sanitize.Line(s.Key)
}

// settingRowID tells the rows apart: the account entries share a key, so
// each is numbered among them. i is the setting's place in list.
func settingRowID(list []ipc.Setting, i int) string {
	if list[i].Key != accountKey {
		return list[i].Key
	}
	n := 0
	for _, s := range list[:i] {
		if s.Key == accountKey {
			n++
		}
	}
	return accountKey + ":" + strconv.Itoa(n)
}

// settingText is a setting's row: label and value, a launch-time
// setting saying so; an account is its value and how to change it.
func settingText(s ipc.Setting) string {
	value := sanitize.Line(s.Value)
	if value == "" {
		value = "(none)"
	}
	switch s.Applies {
	case config.AppliesReadOnly:
		return value + "  " + sanitize.Line(s.Help)
	case config.AppliesLaunch:
		value += "  next launch"
	}
	label := settingLabel(s)
	return label + strings.Repeat(" ", max(labelWidth-len([]rune(label)), 2)) + value
}

// settingListRows are the settings under their headings.
func (m *Model) settingListRows() []ovRow {
	var rows []ovRow
	for _, g := range settingGroups {
		head := false
		for i, s := range m.settings {
			if settingGroup(s.Key) != g {
				continue
			}
			if !head {
				rows, head = append(rows, ovRow{id: "h:" + g, text: g, heading: true}), true
			}
			rows = append(rows, ovRow{id: settingRowID(m.settings, i), text: settingText(s)})
		}
	}
	return rows
}

// setting is the setting key names, the selected one when key is "".
func (m *Model) setting(key string) (ipc.Setting, bool) {
	if key == "" {
		key = m.ovSel().id
	}
	for i, s := range m.settings {
		if settingRowID(m.settings, i) == key {
			return s, true
		}
	}
	return ipc.Setting{}, false
}

func (m *Model) settingsStart(ov *overlay) string {
	ov.sel = slices.IndexFunc(m.settingListRows(), func(r ovRow) bool { return !r.heading })
	return ""
}

func (m *Model) settingsTitle() string {
	if m.ov.step == stepSettings {
		return "Settings"
	}
	if s, ok := m.setting(m.ov.key); ok {
		return settingLabel(s)
	}
	return "Settings"
}

// settingsRows are the settings, or in the choice step the values with
// the current one marked.
func (m *Model) settingsRows() []ovRow {
	switch m.ov.step {
	case stepSettings:
		return m.settingListRows()
	case stepSettingChoice:
		s, _ := m.setting(m.ov.key)
		rows := make([]ovRow, 0, len(s.Choices))
		for _, c := range s.Choices {
			text := sanitize.Line(c)
			if c == s.Value {
				text += "  current"
			}
			rows = append(rows, ovRow{id: "c:" + c, text: text})
		}
		return rows
	}
	return nil
}

// settingsBody is the help of the setting selected or being changed.
func (m *Model) settingsBody(w int) (head []string, empty string, tail []string) {
	key := m.ov.key
	if m.ov.step == stepSettings {
		key = ""
	}
	if m.ov.step == stepSettings && m.settings == nil {
		empty = "reading the settings…"
	}
	if s, ok := m.setting(key); ok && s.Help != "" {
		tail = []string{""}
		for _, l := range wrap(sanitize.Line(s.Help), w) {
			tail = append(tail, m.st.faint.Render(l))
		}
	}
	return nil, empty, tail
}

// settingsSelect selects the row of the setting key, or the first setting.
func (m *Model) settingsSelect(key string) {
	rows := m.settingListRows()
	i := slices.IndexFunc(rows, func(r ovRow) bool { return r.id == key })
	if i < 0 {
		i = slices.IndexFunc(rows, func(r ovRow) bool { return !r.heading })
	}
	m.ov.sel = max(i, 0)
}

// settingsGo opens the selected setting's choice or text step, or, there,
// saves the value.
func (m *Model) settingsGo() tea.Cmd {
	ov := m.ov
	switch ov.step {
	case stepSettingChoice:
		s, _ := m.setting(ov.key)
		v, ok := strings.CutPrefix(m.ovSel().id, "c:")
		if !ok {
			return nil
		}
		if v == s.Value {
			m.ovBack()
			return nil
		}
		return m.saveSetting(s.Key, v)
	case stepSettingText:
		s, _ := m.setting(ov.key)
		return m.saveSetting(s.Key, ov.field.text)
	}
	s, ok := m.setting("")
	switch {
	case !ok:
		return nil
	case s.Applies == config.AppliesReadOnly:
		ov.info = "the account cannot be changed here — " + sanitize.Line(s.Help)
		return nil
	}
	key := m.ovSel().id
	if s.Choices == nil {
		m.ovStepTo(stepSettingText, s.Value)
		ov.key = key
		return nil
	}
	m.ovStepTo(stepSettingChoice, "")
	ov.key = key
	ov.sel = max(slices.Index(s.Choices, s.Value), 0)
	return nil
}

// saveSetting changes key through the background process.
func (m *Model) saveSetting(key, value string) tea.Cmd {
	ov := m.ov
	if m.b == nil || m.procDown {
		ov.err = "background process not running"
		return nil
	}
	ov.busy, ov.err, ov.info = true, "", ""
	b, gen := m.b, ov.gen
	return func() tea.Msg {
		s, err := b.ConfigSet(key, value, configSource)
		return settingSavedMsg{b: b, gen: gen, key: key, s: s, err: err}
	}
}

// onSettingSaved takes a change's answer. A saved value is applied even
// when the overlay has closed since.
func (m *Model) onSettingSaved(msg settingSavedMsg) tea.Cmd {
	if msg.err == nil {
		m.storeSetting(msg.key, msg.s.Value)
	}
	ov := m.ov
	if ov == nil || ov.gen != msg.gen {
		if msg.err == nil {
			return nil
		}
		text := settingFailure(msg.key, msg.err)
		if m.openID == "" {
			return m.setBanner(text)
		}
		return m.setNotice(m.openID, text, true)
	}
	ov.busy = false
	if msg.err != nil {
		ov.err = settingFailure(msg.key, msg.err)
		return nil
	}
	if ov.step != stepSettings {
		m.ovBack()
	}
	return nil
}

// settingFailure words a failed change. A refused value says why in the
// background process's own words.
func settingFailure(key string, err error) string {
	prefix := ipc.ErrInvalid.Error() + ": "
	if errors.Is(err, ipc.ErrInvalid) && strings.HasPrefix(err.Error(), prefix) {
		return sanitize.Line(strings.TrimPrefix(err.Error(), prefix))
	}
	return failure("change "+key, err)
}

// storeSetting records key's new value and applies it.
func (m *Model) storeSetting(key, value string) {
	for i := range m.settings {
		if m.settings[i].Key == key {
			m.settings[i].Value = value
			if m.settings[i].Applies == config.AppliesTUI {
				m.applySetting(m.settings[i])
			}
			return
		}
	}
	if sp, ok := config.SpecFor(key); ok && sp.Applies == config.AppliesTUI {
		m.applySetting(ipc.Setting{Key: key, Value: value, Applies: sp.Applies})
	}
}

// applySetting puts a setting the running interface reads into effect.
func (m *Model) applySetting(s ipc.Setting) {
	switch s.Key {
	case "mark_read":
		m.opts.MarkRead = s.Value == "true"
	case "paste_gap_ms":
		if n, err := strconv.Atoi(s.Value); err == nil {
			m.opts.PasteGap = PasteGapFor(n)
		}
	case "notify.osc":
		m.setOSC(notify.ResolveOSC(s.Value, m.opts.Env))
	}
}

// setOSC changes the notification sequence. Turning notifications on or
// off tells the subscription goroutine, which keeps only the newest value;
// the update loop is the only sender, so this never waits.
func (m *Model) setOSC(osc string) {
	was := m.osc != ""
	m.osc = osc
	if was == (osc != "") {
		return
	}
	select {
	case <-m.renotify:
	default:
	}
	m.renotify <- osc != ""
}

// requestConfig reads the settings; only the newest read is taken.
func (m *Model) requestConfig() tea.Cmd {
	if m.b == nil {
		return nil
	}
	m.cfgSeq++
	b, seq := m.b, m.cfgSeq
	return func() tea.Msg {
		list, err := b.ConfigGet()
		return configMsg{b: b, seq: seq, list: list, err: err}
	}
}

// onConfig takes a read of the settings: the interface applies its own,
// and an open settings overlay keeps its selection on the same setting.
func (m *Model) onConfig(msg configMsg) tea.Cmd {
	if msg.b != m.b || msg.seq != m.cfgSeq {
		return nil
	}
	ov := m.ov
	if ov != nil && ov.kind != ovSettings {
		ov = nil
	}
	if msg.err != nil {
		if ov != nil {
			ov.err = failure("read the settings", msg.err)
		}
		return nil
	}
	var sel string
	if ov != nil && ov.step == stepSettings {
		sel = m.ovSel().id
	}
	m.settings = msg.list
	for _, s := range m.settings {
		if s.Applies == config.AppliesTUI {
			m.applySetting(s)
		}
	}
	if ov != nil && ov.step == stepSettings {
		m.settingsSelect(sel)
	}
	return nil
}
