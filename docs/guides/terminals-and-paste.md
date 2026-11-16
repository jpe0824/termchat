# Terminals and paste

termchat's interface needs only characters, the 16 base colours, and these
terminal features. It uses richer colour where the terminal reports it, and
needs no special font. It has not been tested in every terminal; the list of
what is checked by hand before a release is in
[the manual checklist](manual-checklist.md).

| Feature | Used for | If the terminal lacks it |
|---|---|---|
| Alternate screen | The full-window interface | Required. |
| Mouse reporting | Clicking rows, the input and messages, wheel scrolling | The keyboard does everything the mouse does. |
| Focus reporting | Knowing whether the terminal window is focused, so a message you are looking at is not notified and is marked read | The interface counts as not focused: messages in the open chat still notify, and new ones there are marked read on your next key or mouse input. See [how focus is detected](notifications.md#how-focus-is-detected). |
| Bracketed paste | Pasting text without it being read as typing | See below. |
| Notification sequences (OSC 9, 99, 777) | [Notifications](notifications.md) | The operating-system route is used. |
| Clipboard writes (OSC 52) | `c`, or a second click, copies the selected message | Nothing is copied. The note still shows, because a terminal does not answer OSC 52. |

`alt+enter` inserts a line break. Some terminals use `alt` for other things;
`ctrl+j` does the same everywhere.

## Copying

termchat copies a message by asking the terminal to set the system clipboard
(OSC 52). This reaches your own machine's clipboard even over SSH, because the
terminal on your machine does the copying. Most current terminals allow it;
some ship with clipboard writes off and need them turned on in their settings.
Inside tmux, set `set -g set-clipboard on` in `~/.tmux.conf`. termchat does not
fall back to `pbcopy` or `xclip`: over SSH they would fill the remote machine's
clipboard instead.

## Bracketed paste

In a terminal with bracketed paste (most current terminals, including kitty,
iTerm2, WezTerm, Ghostty, GNOME Terminal, Terminal.app and Windows Terminal;
inside tmux it must be passed through), the terminal marks where a paste starts
and ends. termchat inserts the pasted text into the input exactly as pasted:
line breaks stay as line breaks in the draft and nothing is sent until you press
`enter`. Escape sequences and control characters in the text are dropped.
Tabs are kept and drawn as four spaces.

A paste is inserted only when the input has focus. Anywhere else it is ignored
and the line `paste ignored — press i to type` is shown.

The same rules apply inside the jump, new-conversation, search and room-panel
boxes. A bracketed paste into a text field puts its first line in the field,
where it filters the list or fills the search; it never opens, creates, joins
or invites anything, and a paste into a box with no field is ignored. The keys
that open a box (`ctrl+k`, `ctrl+n`, `ctrl+o`, `/`) and the keys that act inside
one (`enter`, `tab` in the new-room form, `a x n l` in the room panel, `y` to
confirm) act only as a deliberate single press, so a paste without bracketed
paste cannot trigger them either. `esc` closes a box after the same gap.

## Without bracketed paste

A terminal without bracketed paste sends pasted text as if you had typed it, and
termchat cannot tell a pasted line break from the `enter` key. It treats keys
that arrive within `paste_gap_ms` of each other as one paste. What that means:

- **`enter` waits.** After `enter`, termchat waits for the gap. If any key
  arrives in that time, the `enter` becomes a line break in the draft; if none
  does, it sends. So a pasted block of several lines lands as one multi-line
  draft, and nothing is sent until you press `enter` yourself.
- **A paste that ends in a line break sends.** The last line break has nothing
  after it, so it looks like a deliberate `enter`: the text is sent as soon as
  the paste ends. In edit mode, it saves the edit instead.
- **A paste never runs a command.** See [commands and paste](#commands-and-paste).
- **A paste of exactly one command key acts as that key.** A single `q`, `d`,
  `e`, `r`, `x` or `i` pasted while the input is not focused cannot be told from
  a keypress, so it does what that key does. A longer paste of letters never does:
  keys that arrive in a burst are ignored as commands, and a message says
  `press i to type` (or `press enter to open a chat` on the chat list).
- **Control bytes edit the draft.** A control character in pasted text arrives
  as a key, so it does what the key does in the input: `ctrl+h` (backspace)
  and delete erase, `ctrl+w` deletes a word, `ctrl+u` clears the draft, and
  `ctrl+a` and `ctrl+e` move to the start and end of the line.
- **Deleting or discarding a message needs a lone `y`.** Pasted text that
  contains `d` then `y` does not delete anything.
- **Every send waits `paste_gap_ms`.** At the default this is 25 ms.

With bracketed paste none of the above applies, because the terminal says what
is a paste.

### `paste_gap_ms`

```toml
paste_gap_ms = 25
```

| | |
|---|---|
| Default | `25` (milliseconds) |
| Range | 0 to 200; a value outside it is clamped |
| `0` | No waiting and no guards: `enter` sends at once, and every key acts at once. A paste without bracketed paste then sends at each line break. |

Raise it on slow links. Pasted text that arrives in chunks further apart than
the gap can send at each chunk boundary that falls on a line break, which
happens over a slow SSH connection to a terminal without bracketed paste.
Typing is not affected by the gap, except that `enter` and `esc` take effect
after it. A change made with `/settings` or `termchat config set` applies at
once in every running interface.

## Commands and paste

A `/` typed alone into an empty input waits for the paste gap before it starts
a command. If another key arrives in that time, or the `/` came in a paste, it
stays text: a pasted line that starts with `/`, such as `/usr/bin`, sends as a
message. With the gap at `0` a typed `/` starts a command at once.

In a command, an `enter` that ends a paste never runs it. The command stays in
the input with `press enter to run /<name>` (or `press enter to send` for a
`//` draft), and a deliberate `enter` runs it. While the command list shows,
`tab` types a tab when it starts a paste and completes the highlighted command
only as a lone press.

## Why `tab` types a tab and `esc` leaves the input

In the input, every printable key types, because a paste without brackets can
contain any of them. `tab` is one: pasted code often has tabs, so `tab` types a
tab (drawn as four spaces) in every layout rather than moving focus. To leave
the input, press `esc` (or `↑` on an empty draft to select your newest message).
To go to the input, press `i` or `enter`. `tab` moves focus between the chat list
and the thread only when the input does not have focus.

`esc` and `↑` in the input take effect once the gap passes with no further key,
and are ignored when they arrive within the gap of the key before, so a paste
containing an escape or arrow sequence does not move you out of the input.

## Quitting with unsent drafts

`ctrl+c` quits from anywhere. `q` quits when the input does not have focus.
If any conversation has an unsent draft or an edit in progress, the first press
shows a warning for three seconds. For a draft in the conversation you have
open it reads `q again to quit — unsent drafts will be lost` (or `ctrl+c again
to quit`); for a draft in another conversation it names it, for example
`unsent draft in #platform — q again to quit`. Pressing the same key again
within those three seconds quits. Drafts are not saved: they are lost when the
interface closes. The background process keeps running, and so does delivery of
messages you already sent.
