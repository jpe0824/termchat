package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
)

// withSettings gives the fake the settings list a fresh configuration
// shows: the global settings, the fixture's rooms, then the account.
func withSettings(f *fakeBackend) *fakeBackend {
	values := map[string]string{"notify.terminal": "true", "notify.os": "true", "notify.osc": "auto",
		"notify.hook": "", "mark_read": "false", "paste_gap_ms": "25", "splash": "true", "cli.send": "allow"}
	var list []ipc.Setting
	for _, sp := range config.Specs {
		list = append(list, ipc.Setting{Key: sp.Key, Value: values[sp.Key], Choices: sp.Choices,
			Applies: sp.Applies, Help: sp.Help})
	}
	for _, r := range [][2]string{{"r-deploys", "deploys"}, {"r-platform", "platform"}, {"r-random", "random"}} {
		sp, _ := config.SpecFor(config.RoomPrefix + r[0])
		list = append(list, ipc.Setting{Key: sp.Key, Value: "mentions", Choices: sp.Choices,
			Applies: sp.Applies, Help: sp.Help, Room: r[1]})
	}
	list = append(list, ipc.Setting{Key: "account", Value: "jason@team.example:2222",
		Applies: config.AppliesReadOnly, Help: "change with termchat join"})
	f.mu.Lock()
	f.settings = list
	f.mu.Unlock()
	return f
}

// settingsOpen is the settings overlay opened with /settings from
// # platform's input, nothing recorded yet.
func settingsOpen(t *testing.T, w, hgt int) (*fakeBackend, *tester) {
	t.Helper()
	f := withSettings(fixture())
	h := opened(t, f, w, hgt)
	f.resetCalls()
	typeSlash(h)
	h.typeText("settings")
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettings) {
		t.Fatalf("/settings opened %q", ovState(h))
	}
	return f, h
}

// selectSetting moves the selection down to key's row.
func selectSetting(t *testing.T, h *tester, key string) {
	t.Helper()
	for range 30 {
		if h.m.ovSel().id == key {
			return
		}
		h.key("down")
	}
	t.Fatalf("no row for %s: %q", key, rowTexts(h))
}

func TestSettingsOpensFromSlash(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	want := []string{
		"Notifications",
		"notify.terminal  true", "notify.os        true", "notify.osc       auto", "notify.hook      (none)",
		"Interface",
		"mark_read        false", "paste_gap_ms     25", "splash           true  next launch",
		"CLI",
		"cli.send         allow",
		"Rooms",
		"#deploys         mentions", "#platform        mentions", "#random          mentions",
		"Account",
		"jason@team.example:2222  change with termchat join",
	}
	if got := rowTexts(h); !slices.Equal(got, want) {
		t.Errorf("rows:\n%q\nwant\n%q", got, want)
	}
	if h.m.ovSel().id != "notify.terminal" {
		t.Errorf("selected %q, want the first setting", h.m.ovSel().id)
	}
	if w := f.writes(everyWrite...); w != nil {
		t.Errorf("opening wrote %q", w)
	}
	if h.draft() != "" || cmdMode(h) {
		t.Errorf("draft %q cmd %v after /settings", h.draft(), cmdMode(h))
	}
	// Moving up from the first setting stays off its heading.
	h.key("up")
	if h.m.ovSel().id != "notify.terminal" {
		t.Errorf("up from the first setting selected %q", h.m.ovSel().id)
	}
	// Moving past a heading lands on the setting after it.
	selectSetting(t, h, "notify.hook")
	h.key("down")
	if h.m.ovSel().id != "mark_read" {
		t.Errorf("down from notify.hook selected %q, want mark_read", h.m.ovSel().id)
	}
}

func TestSettingsChoiceSaves(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	selectSetting(t, h, "notify.os")
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettingChoice) {
		t.Fatalf("enter on notify.os: %q", ovState(h))
	}
	if got, want := rowTexts(h), []string{"false", "true  current", "always"}; !slices.Equal(got, want) {
		t.Errorf("choices %q, want %q", got, want)
	}
	if h.m.ovSel().id != "c:true" {
		t.Errorf("choice step opened on %q, want the current value", h.m.ovSel().id)
	}
	h.key("down")
	h.enter()
	if got := f.writes("configset "); !slices.Equal(got, []string{"configset notify.os always"}) {
		t.Errorf("writes %q", got)
	}
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettings) || h.m.ovSel().id != "notify.os" {
		t.Errorf("after the save: %q on %q", ovState(h), h.m.ovSel().id)
	}
	if !slices.Contains(rowTexts(h), "notify.os        always") {
		t.Errorf("rows %q lack the new value", rowTexts(h))
	}
}

func TestSettingsTextSaves(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	selectSetting(t, h, "paste_gap_ms")
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettingText) || h.m.ov.field.text != "25" {
		t.Fatalf("enter on paste_gap_ms: %q field %q", ovState(h), h.m.ov.field.text)
	}
	h.key("ctrl+u")
	h.typeText("60")
	h.enter()
	if got := f.writes("configset "); !slices.Equal(got, []string{"configset paste_gap_ms 60"}) {
		t.Errorf("writes %q", got)
	}
	if h.m.opts.PasteGap != 60*time.Millisecond {
		t.Errorf("paste gap %v, want 60ms", h.m.opts.PasteGap)
	}
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettings) || h.m.ovSel().id != "paste_gap_ms" {
		t.Errorf("after the save: %q on %q", ovState(h), h.m.ovSel().id)
	}
	h.enter()
	h.key("ctrl+u")
	h.typeText("0")
	h.enter()
	if h.m.opts.PasteGap != -1 {
		t.Errorf("paste gap %v after 0, want -1 (off)", h.m.opts.PasteGap)
	}
}

func TestSettingsLiveInterfaceKeys(t *testing.T) {
	_, h := settingsOpen(t, 90, 30)
	h.m.applySetting(ipc.Setting{Key: "mark_read", Value: "true", Applies: config.AppliesTUI})
	if !h.m.opts.MarkRead {
		t.Error("mark_read true not applied")
	}
	// notify.osc from none to a sequence, and back, tells the subscription.
	h.m.applySetting(ipc.Setting{Key: "notify.osc", Value: "99", Applies: config.AppliesTUI})
	if h.m.osc != "99" {
		t.Errorf("osc %q, want 99", h.m.osc)
	}
	if v, ok := recvBool(h.m.renotify); !ok || !v {
		t.Errorf("renotify %v %v, want true", v, ok)
	}
	h.m.applySetting(ipc.Setting{Key: "notify.osc", Value: "777", Applies: config.AppliesTUI})
	if v, ok := recvBool(h.m.renotify); ok {
		t.Errorf("99 to 777 renotified %v; the route did not change", v)
	}
	h.m.applySetting(ipc.Setting{Key: "notify.osc", Value: "off", Applies: config.AppliesTUI})
	if v, ok := recvBool(h.m.renotify); !ok || v || h.m.osc != "" {
		t.Errorf("off: renotify %v %v osc %q", v, ok, h.m.osc)
	}
}

func recvBool(ch chan bool) (bool, bool) {
	select {
	case v := <-ch:
		return v, true
	default:
		return false, false
	}
}

func TestSettingsErrorKeepsStep(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	f.failWith("configset", fmt.Errorf("%w: paste_gap_ms: \"x\" is not a whole number", ipc.ErrInvalid))
	selectSetting(t, h, "paste_gap_ms")
	h.enter()
	h.key("ctrl+u")
	h.typeText("x")
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettingText) || h.m.ov.field.text != "x" {
		t.Errorf("after a failed save: %q field %q", ovState(h), h.m.ov.field.text)
	}
	if want := `paste_gap_ms: "x" is not a whole number`; h.m.ov.err != want {
		t.Errorf("error %q, want %q", h.m.ov.err, want)
	}
	mustContain(t, ovFlat(h), `paste_gap_ms: "x" is not a whole number`)
	if h.m.opts.PasteGap != defaultPasteGap {
		t.Errorf("a failed save changed the paste gap to %v", h.m.opts.PasteGap)
	}
}

func TestSettingsReadOnlyAccount(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	selectSetting(t, h, "account:0")
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettings) {
		t.Errorf("enter on the account opened %q", ovState(h))
	}
	if !strings.Contains(h.m.ov.info, "change with termchat join") {
		t.Errorf("hint %q", h.m.ov.info)
	}
	if w := f.writes(everyWrite...); w != nil {
		t.Errorf("wrote %q", w)
	}
}

func TestConfigEventRereads(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	selectSetting(t, h, "paste_gap_ms")
	f.mu.Lock()
	for i := range f.settings {
		if f.settings[i].Key == "paste_gap_ms" {
			f.settings[i].Value = "80"
		}
	}
	f.mu.Unlock()
	n := f.callCount("configget")
	h.send(eventMsg{ipc.Event{Kind: ipc.KindConfig}})
	if f.callCount("configget") != n+1 {
		t.Errorf("config event made %d reads", f.callCount("configget")-n)
	}
	if !slices.Contains(rowTexts(h), "paste_gap_ms     80") {
		t.Errorf("rows %q lack the new value", rowTexts(h))
	}
	if h.m.opts.PasteGap != 80*time.Millisecond {
		t.Errorf("paste gap %v, want 80ms", h.m.opts.PasteGap)
	}
	if h.m.ovSel().id != "paste_gap_ms" {
		t.Errorf("selection moved to %q", h.m.ovSel().id)
	}
	// With the overlay closed the interface still takes the new values.
	h.key("esc")
	f.mu.Lock()
	for i := range f.settings {
		if f.settings[i].Key == "mark_read" {
			f.settings[i].Value = "true"
		}
	}
	f.mu.Unlock()
	h.send(eventMsg{ipc.Event{Kind: ipc.KindConfig}})
	if !h.m.opts.MarkRead {
		t.Error("mark_read not applied with the overlay closed")
	}
}

func TestSettingsSanitizesRoomNames(t *testing.T) {
	f := withSettings(fixture())
	f.mu.Lock()
	for i := range f.settings {
		if f.settings[i].Room == "random" {
			f.settings[i].Room = "ran\x1b[31mdom"
		}
	}
	f.mu.Unlock()
	h := opened(t, f, 90, 30)
	typeSlash(h)
	h.typeText("settings")
	h.enter()
	for _, r := range rowTexts(h) {
		if strings.ContainsRune(r, '\x1b') {
			t.Errorf("row %q carries an escape", r)
		}
	}
	if strings.Contains(h.raw(), "\x1b[31m") {
		t.Error("the frame carries the room name's escape")
	}
}

func TestPasteGapFor(t *testing.T) {
	for ms, want := range map[int]time.Duration{25: 25 * time.Millisecond, 80: 80 * time.Millisecond, 0: -1, -5: -1} {
		if got := PasteGapFor(ms); got != want {
			t.Errorf("PasteGapFor(%d) = %v, want %v", ms, got, want)
		}
	}
}

func TestGoldenSettings(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"wide", 112, 16},
		{"standard", 84, 14},
		{"narrow", 44, 14},
		{"tiny", 30, 14},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, h := settingsOpen(t, c.w, c.h)
			checkGolden(t, "settings-"+c.name, h.view()+"\n")
		})
	}
	t.Run("choice", func(t *testing.T) {
		_, h := settingsOpen(t, 84, 14)
		selectSetting(t, h, "notify.os")
		h.enter()
		checkGolden(t, "settings-choice-standard", h.view()+"\n")
	})
}

func TestSettingsRoomGoneNamesTheRoom(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	f.failWith("configset", ipc.ErrNotFound)
	selectSetting(t, h, config.RoomPrefix+"r-random")
	h.enter()
	h.key("down")
	h.enter()
	if want := "can't change #random: not in that room any more"; h.m.ov.err != want {
		t.Errorf("error %q, want %q", h.m.ov.err, want)
	}
}

func TestSettingsSaveBeatsOlderRead(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	h.m.requestConfig()
	stale := configMsg{b: h.m.b, seq: h.m.cfgSeq, list: slices.Clone(h.m.settings)}
	selectSetting(t, h, "paste_gap_ms")
	h.enter()
	h.key("ctrl+u")
	h.typeText("80")
	h.enter()
	if got := f.writes("configset "); !slices.Equal(got, []string{"configset paste_gap_ms 80"}) {
		t.Fatalf("writes %q", got)
	}
	h.send(stale)
	if !slices.Contains(rowTexts(h), "paste_gap_ms     80") {
		t.Errorf("rows %q lost the saved value", rowTexts(h))
	}
	if h.m.opts.PasteGap != 80*time.Millisecond {
		t.Errorf("paste gap reverted to %v", h.m.opts.PasteGap)
	}
}

func TestSettingsHeadingStaysWithItsFirstRow(t *testing.T) {
	_, h := settingsOpen(t, 44, 9)
	for range 12 {
		h.key("down")
	}
	h.key("home")
	if !strings.Contains(h.view(), "Notifications") {
		t.Errorf("home hid the first heading:\n%s", h.view())
	}
	// Moving up onto a later group's first row keeps its heading too.
	selectSetting(t, h, "mark_read")
	h.key("down")
	h.key("up")
	if !strings.Contains(h.view(), "Interface") {
		t.Errorf("the Interface heading is hidden:\n%s", h.view())
	}
}
