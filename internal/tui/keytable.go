package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

var (
	moveKeys = []binding{
		{keys: "↑ ↓", desc: "move", match: []string{"up", "down"}, foot: "↑↓ move", run: (*Model).ovMoveKey},
		{keys: "pgup pgdn", desc: "move a page", match: []string{"pgup", "pgdown"}, run: (*Model).ovMoveKey},
	}
	listKeys = []binding{
		{keys: "k j home end", desc: "move, to the first, to the last (lists without a text field)",
			match: []string{"k", "j", "home", "end"}, run: (*Model).ovMoveKey},
	}
	fieldKeys = binding{keys: "letters", desc: "type; ← → home end backspace ctrl+w ctrl+u edit; tab does nothing"}
)

// keyTable is every key the interface takes, by where the keyboard is.
var keyTable = []keyGroup{
	{name: "Anywhere", main: mainAnywhere, binds: []binding{
		{keys: "?", desc: "help; ? or esc closes it, ↑ ↓ pgup pgdn scroll it (outside a text field)",
			match: []string{"?"}, run: (*Model).keyHelp},
		{keys: "ctrl+k", desc: "jump to a conversation or a person", kind: keyGuarded, match: []string{"ctrl+k"},
			run: overlayRun(ovJump)},
		{keys: "ctrl+n", desc: "new conversation: direct message, room, or join an open room", kind: keyGuarded,
			match: []string{"ctrl+n"}, run: overlayRun(ovNew)},
		{keys: "ctrl+o", desc: "room panel: members, invite, remove, rename, leave", kind: keyGuarded,
			match: []string{"ctrl+o"}, run: overlayRun(ovRoom)},
		{keys: "ctrl+c", desc: "quit; asked twice when drafts are unsent"},
		{keys: "mouse", desc: "click a chat, a message, the input or an overlay row; a second click on a row acts, and on a message copies it; click outside an overlay to close it; the wheel scrolls"},
	}},
	{name: "Chat list", main: mainList, binds: []binding{
		{keys: "↑ ↓ k j", desc: "move", match: []string{"up", "down", "k", "j"}, run: (*Model).keyMove},
		{keys: "enter", desc: "open the chat", kind: keyGuarded, match: []string{"enter"},
			run: (*Model).keyListEnter},
		{keys: "tab", desc: "to the thread (standard and wide layouts)", kind: keyGuarded, match: []string{"tab"},
			run: (*Model).keyTab},
		{keys: "/", desc: "search history", kind: keyGuarded, match: []string{"/"}, run: (*Model).keySearch},
		{keys: "pgup pgdn", desc: "scroll the thread; home end: oldest, newest",
			match: []string{"pgup", "pgdown", "home", "end"}, run: (*Model).keyPage},
		{keys: "r", desc: "start a stopped background process", kind: keyGuarded, match: []string{"r"}, run: (*Model).keyR},
		{keys: "q", desc: "quit", kind: keyGuarded, match: []string{"q"}, run: (*Model).keyQuit},
	}},
	{name: "Thread", main: mainThread, binds: []binding{
		{keys: "↑ ↓ k j", desc: "step through messages, or scroll", match: []string{"up", "down", "k", "j"},
			run: (*Model).keyMove},
		{keys: "i enter", desc: "go to the input — letters type only in the input", kind: keyGuarded,
			match: []string{"i", "enter"}, run: (*Model).keyThreadEnter},
		{keys: "e", desc: "edit your selected message", kind: keyGuarded, match: []string{"e"}, run: (*Model).keyEdit},
		{keys: "d", desc: "delete your selected message, after y/n", kind: keyGuarded, match: []string{"d"},
			run: (*Model).keyDelete},
		{keys: "r", desc: "retry a failed message; start a stopped background process", kind: keyGuarded,
			match: []string{"r"}, run: (*Model).keyR},
		{keys: "x", desc: "discard a failed message, after y/n", kind: keyGuarded, match: []string{"x"},
			run: (*Model).keyDiscard},
		{keys: "c", desc: "copy the selected message", kind: keyGuarded, match: []string{"c"},
			run: (*Model).keyCopy},
		{keys: "esc", desc: "from a selection to the input, else to the chat list", match: []string{"esc"},
			run: (*Model).keyEsc},
		{keys: "tab", desc: "to the chat list", kind: keyGuarded, match: []string{"tab"}, run: (*Model).keyTab},
		{keys: "/", desc: "search history", kind: keyGuarded, match: []string{"/"}, run: (*Model).keySearch},
		{keys: "pgup pgdn", desc: "scroll; home end: oldest, newest message",
			match: []string{"pgup", "pgdown", "home", "end"}, run: (*Model).keyPage},
		{keys: "q", desc: "quit", kind: keyGuarded, match: []string{"q"}, run: (*Model).keyQuit},
	}},
	{name: "Input", binds: []binding{
		{keys: "letters", desc: "type; ← → home end ctrl+a ctrl+e backspace delete ctrl+w ctrl+u edit"},
		{keys: "enter", desc: "send, or save an edit"},
		{keys: "alt+enter", desc: "new line (also ctrl+j)"},
		{keys: "esc", desc: "cancel an edit, else leave the input"},
		{keys: "↑", desc: "in an empty input: select your newest message"},
		{keys: "tab", desc: "types a tab"},
		{keys: "pgup pgdn", desc: "scroll the thread"},
		{keys: "/", desc: "typed alone in an empty input: a command, with the list of commands; // sends a leading /"},
	}},
	{name: "Command list", main: mainCmdList, binds: []binding{
		{keys: "↑ ↓", desc: "move the highlight", match: []string{"up", "down"}, run: (*Model).cmdListMove,
			foot: "↑↓ pick"},
		{keys: "tab", desc: "complete the highlighted command; inside a paste it types a tab", kind: keyGuarded,
			match: []string{"tab"}, run: (*Model).cmdListComplete, foot: "tab complete"},
		{keys: "enter", desc: "complete a name typed in part, run a whole one", foot: "⏎ run"},
		{keys: "esc", desc: "hide the list and keep the text; esc again leaves the input", kind: keyDeferred,
			match: []string{"esc"}, run: (*Model).cmdListHide, foot: "esc hide"},
	}},
	{name: "Questions (y/n)", binds: []binding{
		{keys: "y", desc: "yes, as a lone key press"},
		{keys: "any other", desc: "no"},
	}},
	{name: "Jump (ctrl+k)", kind: ovJump, binds: slices.Concat([]binding{fieldKeys}, moveKeys, []binding{
		{keys: "enter", desc: "open the selected conversation, or start a DM with the person",
			kind: keyGuarded, match: []string{"enter"}, run: (*Model).ovGoKey, foot: "⏎ open"},
		{keys: "esc", desc: "close", kind: keyGuarded, match: []string{"esc"}, run: (*Model).ovBackKey, foot: "esc close"},
	})},
	{name: "New conversation (ctrl+n)", kind: ovNew, binds: slices.Concat(moveKeys, listKeys, []binding{
		{keys: "enter", desc: "choose", kind: keyGuarded, match: []string{"enter"}, run: (*Model).ovGoKey,
			foot: "⏎ choose", steps: []ovStep{stepChoose}},
		{keys: "enter", desc: "direct message: open the DM with the selected person", kind: keyGuarded,
			match: []string{"enter"}, run: (*Model).ovGoKey, foot: "⏎ open", steps: []ovStep{stepDM}},
		{keys: "enter", desc: "new room: create it", kind: keyGuarded, match: []string{"enter"}, run: (*Model).ovGoKey,
			foot: "⏎ create", steps: []ovStep{stepRoom}},
		{keys: "tab", desc: "new room: open or private", kind: keyGuarded, match: []string{"tab"},
			run: (*Model).ovToggleKey, foot: "tab open/private", steps: []ovStep{stepRoom}},
		{keys: "enter", desc: "join the selected open room", kind: keyGuarded, match: []string{"enter"},
			run: (*Model).ovGoKey, foot: "⏎ join", steps: []ovStep{stepJoin}},
		{keys: "esc", desc: "back to the choices, or close", kind: keyGuarded, match: []string{"esc"},
			run: (*Model).ovBackKey, foot: "esc back"},
		fieldKeys,
	})},
	{name: "Search (/)", kind: ovSearch, binds: slices.Concat([]binding{fieldKeys}, moveKeys, []binding{
		{keys: "enter", desc: "open the conversation at that message", kind: keyGuarded,
			match: []string{"enter"}, run: (*Model).ovGoKey, foot: "⏎ open"},
		{keys: "esc", desc: "close", kind: keyGuarded, match: []string{"esc"}, run: (*Model).ovBackKey, foot: "esc close"},
	})},
	{name: "Room panel (ctrl+o)", kind: ovRoom, binds: slices.Concat(moveKeys, listKeys, []binding{
		{keys: "a", desc: "invite someone (creator and admins)", kind: keyGuarded, match: []string{"a"},
			run: roomRun((*Model).roomInvite), foot: "a invite", steps: []ovStep{stepMembers}, manage: true},
		{keys: "x", desc: "remove the selected member, after y/n (creator and admins)", kind: keyGuarded,
			match: []string{"x"}, run: roomRun((*Model).roomRemove), foot: "x remove", steps: []ovStep{stepMembers}, manage: true},
		{keys: "n", desc: "rename the room (creator and admins)", kind: keyGuarded, match: []string{"n"},
			run: roomRun((*Model).roomRename), foot: "n rename", steps: []ovStep{stepMembers}, manage: true},
		{keys: "l", desc: "leave the room, after y/n", kind: keyGuarded, match: []string{"l"}, run: roomRun((*Model).roomLeave),
			foot: "l leave", steps: []ovStep{stepMembers}},
		{keys: "enter", desc: "act on the selected row", kind: keyGuarded, match: []string{"enter"}, run: (*Model).ovGoKey,
			steps: []ovStep{stepMembers}},
		{keys: "enter", desc: "invite: invite the selected person", kind: keyGuarded, match: []string{"enter"},
			run: (*Model).ovGoKey, foot: "⏎ invite", steps: []ovStep{stepInvite}},
		{keys: "enter", desc: "rename: save the new name", kind: keyGuarded, match: []string{"enter"},
			run: (*Model).ovGoKey, foot: "⏎ rename", steps: []ovStep{stepRename}},
		{keys: "esc", desc: "back to the members, or close", kind: keyGuarded, match: []string{"esc"},
			run: (*Model).ovBackKey, foot: "esc back"},
		fieldKeys,
	})},
	{name: "Settings (/settings)", kind: ovSettings, binds: slices.Concat(
		onSteps(slices.Concat(moveKeys, listKeys), stepSettings, stepSettingChoice), []binding{
			{keys: "enter", desc: "change the selected setting: pick a value, or edit text", kind: keyGuarded,
				match: []string{"enter"}, run: (*Model).ovGoKey, foot: "⏎ change", steps: []ovStep{stepSettings}},
			{keys: "enter", desc: "save the selected value, or the text", kind: keyGuarded, match: []string{"enter"},
				run: (*Model).ovGoKey, foot: "⏎ save", steps: []ovStep{stepSettingChoice, stepSettingText}},
			{keys: "esc", desc: "back to the settings, or close", kind: keyGuarded, match: []string{"esc"},
				run: (*Model).ovBackKey, foot: "esc back"},
			fieldKeys,
		})},
}

// keyCtx is what a key acts on, as it stands when the key acts.
type keyCtx struct {
	key    string
	g      geom
	list   bool
	sel    item
	hasSel bool
}

// overlayRun opens overlay kind.
func overlayRun(kind ovKind) func(*Model, keyCtx) tea.Cmd {
	return func(m *Model, _ keyCtx) tea.Cmd { return m.openOverlay(kind) }
}

// roomRun starts one of the room panel's actions.
func roomRun(f func(*Model) tea.Cmd) func(*Model, keyCtx) tea.Cmd {
	return func(m *Model, _ keyCtx) tea.Cmd { return f(m) }
}

// onSteps is binds limited to an overlay's steps.
func onSteps(binds []binding, steps ...ovStep) []binding {
	out := slices.Clone(binds)
	for i := range out {
		out[i].steps = steps
	}
	return out
}
