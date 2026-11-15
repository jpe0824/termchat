package tui

import "slices"

var (
	moveKeys = []binding{
		{keys: "↑ ↓", desc: "move", match: []string{"up", "down"}, foot: "↑↓ move"},
		{keys: "pgup pgdn", desc: "move a page", match: []string{"pgup", "pgdown"}},
	}
	listKeys = []binding{
		{keys: "k j home end", desc: "move, to the first, to the last (lists without a text field)",
			match: []string{"k", "j", "home", "end"}},
	}
	fieldKeys = binding{keys: "letters", desc: "type; ← → home end backspace ctrl+w ctrl+u edit; tab does nothing"}
)

// keyTable is every key the interface takes, by where the keyboard is.
var keyTable = []keyGroup{
	{name: "Anywhere", binds: []binding{
		{keys: "?", desc: "help; ? or esc closes it, ↑ ↓ pgup pgdn scroll it (outside a text field)",
			match: []string{"?"}, act: actHelp},
		{keys: "ctrl+k", desc: "jump to a conversation or a person", kind: keyGuarded, match: []string{"ctrl+k"},
			act: actJump},
		{keys: "ctrl+n", desc: "new conversation: direct message, room, or join an open room", kind: keyGuarded,
			match: []string{"ctrl+n"}, act: actNew},
		{keys: "ctrl+o", desc: "room panel: members, invite, remove, rename, leave", kind: keyGuarded,
			match: []string{"ctrl+o"}, act: actPanel},
		{keys: "ctrl+c", desc: "quit; asked twice when drafts are unsent", kind: keyDeferred},
		{keys: "mouse", desc: "click a chat, a message, the input or an overlay row; a second click on a row acts; click outside an overlay to close it; the wheel scrolls"},
	}},
	{name: "Chat list", binds: []binding{
		{keys: "↑ ↓ k j", desc: "move", match: []string{"up", "down", "k", "j"}, act: actMove},
		{keys: "enter", desc: "open the chat", kind: keyGuarded, match: []string{"enter"}, act: actEnter},
		{keys: "tab", desc: "to the thread (standard and wide layouts)", kind: keyGuarded, match: []string{"tab"},
			act: actTab},
		{keys: "/", desc: "search history", kind: keyGuarded, match: []string{"/"}, act: actSearch},
		{keys: "pgup pgdn", desc: "scroll the thread; home end: oldest, newest",
			match: []string{"pgup", "pgdown", "home", "end"}, act: actPage},
		{keys: "r", desc: "start a stopped background process", kind: keyGuarded, match: []string{"r"}, act: actR},
		{keys: "q", desc: "quit", kind: keyGuarded, match: []string{"q"}, act: actQuit},
	}},
	{name: "Thread", binds: []binding{
		{keys: "↑ ↓ k j", desc: "step through your messages, or scroll", match: []string{"up", "down", "k", "j"},
			act: actMove},
		{keys: "i enter", desc: "go to the input — letters type only in the input", kind: keyGuarded,
			match: []string{"i", "enter"}, act: actEnter},
		{keys: "e", desc: "edit your selected message", kind: keyGuarded, match: []string{"e"}, act: actEdit},
		{keys: "d", desc: "delete your selected message, after y/n", kind: keyGuarded, match: []string{"d"},
			act: actDelete},
		{keys: "r", desc: "retry a failed message; start a stopped background process", kind: keyGuarded,
			match: []string{"r"}, act: actR},
		{keys: "x", desc: "discard a failed message, after y/n", kind: keyGuarded, match: []string{"x"},
			act: actDiscard},
		{keys: "esc", desc: "from a selection to the input, else to the chat list", match: []string{"esc"},
			act: actEsc},
		{keys: "tab", desc: "to the chat list", kind: keyGuarded, match: []string{"tab"}, act: actTab},
		{keys: "/", desc: "search history", kind: keyGuarded, match: []string{"/"}, act: actSearch},
		{keys: "pgup pgdn", desc: "scroll; home end: oldest, newest message",
			match: []string{"pgup", "pgdown", "home", "end"}, act: actPage},
		{keys: "q", desc: "quit", kind: keyGuarded, match: []string{"q"}, act: actQuit},
	}},
	{name: "Input", binds: []binding{
		{keys: "letters", desc: "type; ← → home end ctrl+a ctrl+e backspace delete ctrl+w ctrl+u edit"},
		{keys: "enter", desc: "send, or save an edit", kind: keyDeferred},
		{keys: "alt+enter", desc: "new line (also ctrl+j)"},
		{keys: "esc", desc: "cancel an edit, else leave the input", kind: keyDeferred},
		{keys: "↑", desc: "in an empty input: select your newest message", kind: keyDeferred},
		{keys: "tab", desc: "types a tab"},
		{keys: "pgup pgdn", desc: "scroll the thread"},
	}},
	{name: "Questions (y/n)", binds: []binding{
		{keys: "y", desc: "yes, as a lone key press", kind: keyGuarded},
		{keys: "any other", desc: "no"},
	}},
	{name: "Jump (ctrl+k)", kind: ovJump, binds: slices.Concat([]binding{fieldKeys}, moveKeys, []binding{
		{keys: "enter", desc: "open the selected conversation, or start a DM with the person",
			kind: keyGuarded, match: []string{"enter"}, act: actGo, foot: "⏎ open"},
		{keys: "esc", desc: "close", kind: keyDeferred, match: []string{"esc"}, act: actBack, foot: "esc close"},
	})},
	{name: "New conversation (ctrl+n)", kind: ovNew, binds: slices.Concat(moveKeys, listKeys, []binding{
		{keys: "enter", desc: "choose", kind: keyGuarded, match: []string{"enter"}, act: actGo,
			foot: "⏎ choose", steps: []ovStep{stepChoose}},
		{keys: "enter", desc: "direct message: open the DM with the selected person", kind: keyGuarded,
			match: []string{"enter"}, act: actGo, foot: "⏎ open", steps: []ovStep{stepDM}},
		{keys: "enter", desc: "new room: create it", kind: keyGuarded, match: []string{"enter"}, act: actGo,
			foot: "⏎ create", steps: []ovStep{stepRoom}},
		{keys: "tab", desc: "new room: open or private", kind: keyGuarded, match: []string{"tab"},
			act: actToggle, foot: "tab open/private", steps: []ovStep{stepRoom}},
		{keys: "enter", desc: "join the selected open room", kind: keyGuarded, match: []string{"enter"},
			act: actGo, foot: "⏎ join", steps: []ovStep{stepJoin}},
		{keys: "esc", desc: "back to the choices, or close", kind: keyDeferred, match: []string{"esc"},
			act: actBack, foot: "esc back"},
		fieldKeys,
	})},
	{name: "Search (/)", kind: ovSearch, binds: slices.Concat([]binding{fieldKeys}, moveKeys, []binding{
		{keys: "enter", desc: "open the conversation at that message", kind: keyGuarded,
			match: []string{"enter"}, act: actGo, foot: "⏎ open"},
		{keys: "esc", desc: "close", kind: keyDeferred, match: []string{"esc"}, act: actBack, foot: "esc close"},
	})},
	{name: "Room panel (ctrl+o)", kind: ovRoom, binds: slices.Concat(moveKeys, listKeys, []binding{
		{keys: "a", desc: "invite someone (creator and admins)", kind: keyGuarded, match: []string{"a"},
			act: actInvite, foot: "a invite", steps: []ovStep{stepMembers}, manage: true},
		{keys: "x", desc: "remove the selected member, after y/n (creator and admins)", kind: keyGuarded,
			match: []string{"x"}, act: actRemove, foot: "x remove", steps: []ovStep{stepMembers}, manage: true},
		{keys: "n", desc: "rename the room (creator and admins)", kind: keyGuarded, match: []string{"n"},
			act: actRename, foot: "n rename", steps: []ovStep{stepMembers}, manage: true},
		{keys: "l", desc: "leave the room, after y/n", kind: keyGuarded, match: []string{"l"}, act: actLeave,
			foot: "l leave", steps: []ovStep{stepMembers}},
		{keys: "enter", desc: "act on the selected row", kind: keyGuarded, match: []string{"enter"}, act: actGo,
			steps: []ovStep{stepMembers}},
		{keys: "enter", desc: "invite: invite the selected person", kind: keyGuarded, match: []string{"enter"},
			act: actGo, foot: "⏎ invite", steps: []ovStep{stepInvite}},
		{keys: "enter", desc: "rename: save the new name", kind: keyGuarded, match: []string{"enter"},
			act: actGo, foot: "⏎ rename", steps: []ovStep{stepRename}},
		{keys: "esc", desc: "back to the members, or close", kind: keyDeferred, match: []string{"esc"},
			act: actBack, foot: "esc back"},
		fieldKeys,
	})},
}
