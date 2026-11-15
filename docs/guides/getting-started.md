# Getting started with the termchat client

This guide covers joining a server, the background process, the interface, and
the config file. To run a server, see [the server guide](server.md). To script
termchat, see [the agent CLI guide](agent-cli.md).

## Join a server

You need the server's address and an invite code from an admin. Install
termchat ([README](../../README.md#install)) and run:

```sh
termchat join chat.internal:7420 --invite 4N5FHFRB65
```

In a terminal it prints `joined chat.internal:7420 as @bob` and the server's
key fingerprint, `server key SHA256:...`; piped, it prints the status as JSON.
Check the fingerprint against the one the server's operator sees (`termchat
serve` prints it as `host key SHA256:...`). The address is `host:port`; with
no port, 7420 is used.

`join` creates a key for this device (in your data directory, mode 0600),
records the server's host key so later changes are noticed, registers the key
under your handle, and saves the account in `config.toml`. This version
connects to one server.

Then open the interface:

```sh
termchat
```

With no arguments in a terminal, `termchat` opens the interface; piped, it
prints help. `termchat tui` always opens it. Run with no account joined, the
opening screen stays up and shows the `join` command.

### A second device

On a device that is already signed in:

```sh
termchat device add
```

It prints an invite code and the `termchat join ...` line to run on the other
device. Each device has its own key; an admin can revoke one without affecting
the others.

## The background process

The first time you run any client command or the interface, termchat starts a
background process (`termchat daemon run`, which you never run yourself). It
keeps the connection to the server, your local history and outbox, and sends
notifications. It keeps running after you close the interface, so notifications
arrive with the interface closed.

```sh
termchat daemon start    # make sure it is running; prints the connection state
termchat status          # account, handle, state, pending messages
termchat daemon stop     # stop it; exits 0 if none is running
```

`status` reports one of `connecting`, `connected`, `offline`, `not_authorised`
(your key was removed), `host_key_changed` or `version_mismatch`, and the
pinned `server key`. Messages sent while offline wait in the outbox and are
delivered when the connection returns. After `not_authorised` or
`host_key_changed`, join the same server again with a new invite (see the
[server guide](server.md#a-device-that-was-revoked)).

Stop the background process after upgrading termchat, and after editing
`config.toml` (it reads the file at start). The next command starts a new one.
termchat does not install a login item; the process starts the first time you run a client command after a reboot. It is not supported on Windows.

Files: `client.db`, the device keys in `keys/`, `known_servers` and
`daemon.log` are in the data directory; the socket and lock are in the runtime
directory. See [architecture](../termchat.md#where-state-lives).

## The interface

The sample frame in the [README](../../README.md) shows the wide layout: chat
list on the left, the open conversation in the middle, members on the right,
the input at the bottom of the thread, and the keys that work right now in the
footer. The top bar shows the server and the connection state.

### Layouts by width

The layout follows the window size and redraws on every resize.

| Columns | Layout |
|---|---|
| 110 and up (wide) | Chat list, thread and a members panel for rooms. |
| 70 to 109 (standard) | Chat list and thread. |
| 36 to 69 (narrow) | The thread only. A `‹ chats ●7` marker in its header shows unread messages elsewhere; `esc` or a click on it shows the chat list full-width. |
| under 36 (tiny) | The thread with timestamps dropped and each author on their own line, for a side pane. |

In short windows the footer goes first, then the top bar, so the thread and
input stay visible. Below 20 columns by 5 rows the screen only says the window
is too small.

### Opening screen

The `TERMCHAT` banner shows for 0.7 seconds or until a key or click. Set
`splash = false` to skip it. Under 72 columns it shows plain spaced letters.

### Focus: list, thread, input

Keyboard focus is in one of three places, and the footer always lists the keys
that work there. `?` opens the same table inside the program (outside the
input, where letters type). Every key below comes from one key table in the
code, so the help overlay and this guide agree.

| Focus | Keys |
|---|---|
| **Chat list** | `↑ ↓` or `k j` move; `enter` opens the chat; `tab` goes to the thread (standard and wide layouts); `/` search; `pgup pgdn` scroll the thread, `home end` jump to its oldest and newest message; `r` starts a stopped background process; `q` quits. |
| **Thread** | `↑ ↓` or `k j` step through your own messages once one is selected, otherwise scroll; `i` or `enter` go to the input; `e` edit and `d` delete your selected message (`d` asks y/n); `r` retry a failed message, or start a stopped background process; `x` discard a failed message (asks y/n); `esc` goes from a selection to the input, otherwise to the chat list; `tab` goes to the chat list; `/` search; `pgup pgdn home end` scroll; `q` quits. |
| **Input** | Letters type, including `q` and `?`. `← →`, `home end`, `ctrl+a ctrl+e`, `backspace`, `delete`, `ctrl+w` and `ctrl+u` edit. `enter` sends (or saves an edit); `alt+enter` or `ctrl+j` inserts a line break; `esc` cancels an edit, otherwise leaves the input; `↑` in an empty input selects your newest message; `tab` types a tab; `pgup pgdn` scroll the thread. |
| **A y/n question** | `y` answers yes, only as a lone key press. Any other key answers no. |
| **Anywhere** | `ctrl+k` jump, `ctrl+n` new conversation, `ctrl+o` room panel, `/` search (not from the input), `ctrl+c` quits. |

Outside the input only these commands act. A letter that is not a command
shows a short hint instead of doing anything: `press i to type` on the thread,
`press enter to open a chat` on the chat list. `i` or `enter` on the thread is
the way to the input, `esc` the way out, and `tab` never moves focus out of the
input, because it types a tab there.

Some keys act only as a deliberate single press: `ctrl+k`, `ctrl+n`, `ctrl+o`,
`enter` on the chat list, `tab` outside the input, `/`, `r`, `q`, `i`, `e`, `d`,
`x`, `y`, and the action keys in overlays. A key of this kind is ignored when
another key arrives within `paste_gap_ms` before or after it, so pasted text
cannot trigger it; `enter`, `esc` and `↑` in the input, `ctrl+c`, and `esc` in
overlays wait for the same gap before acting. Movement keys and `?` act at
once. See [terminals and paste](terminals-and-paste.md).

The conversation shown at launch is a preview: it is on screen but is not
marked read and does not take the keyboard until you act on it (open it with
`enter` or a click, press `i` or `enter` on its thread, click in it, or scroll
it). Opening a conversation from the list puts focus in the input. The mouse
works everywhere: click a row to open it, click the input to type, click your
own message to select it, use the wheel to scroll.

### Sending

- `enter` sends the draft. `alt+enter` or `ctrl+j` inserts a line break.
- A message is shown at once as `sending…`. Empty text sends nothing. The
  limit is 8192 bytes; above 7000 a counter shows beside the input and a
  message over the limit is not sent.
- `@handle` mentions a person; mentioned people are notified in rooms.
- Each conversation keeps its own draft while the interface is open. Drafts
  are not saved when it closes.
- If the background process is down, the input is dimmed and typing is ignored.
  The draft is kept; press `r` to start the process, or `esc` to leave the input.

### Editing and deleting your messages

In the input, `↑` on an empty draft selects your newest message; the focus is
then the thread with a selection. `↑ ↓` or `k j` move among your own messages,
and `esc` returns to the input.

- `e` loads a delivered message into the input for editing. `enter` saves,
  `esc` cancels. Edited messages show `(edited)`.
- `d` asks `delete this message? y/n`. Deleted messages stay as `message
  deleted`; the old text is removed from the server and from every client.

### Failed messages, retry and discard

A message that could not be delivered shows `failed (reason) — retry?`, for
example `failed (expired) — retry?`. A message that waits more than ten minutes
for the connection fails as expired instead of going out late. Select it:

- `r` (or a click) retries it.
- `x` asks `discard this message? y/n` and removes it.

Nothing is ever resent automatically after it has failed, and a message is
never delivered twice.

### Jump, new conversation, search, room panel, help

These open as a box in the middle of the window, in every layout. Each needs
the background process to be running, and closes with `esc` (or a click outside
the box). In a box with a text field (jump, search, the pickers), letters type,
`← →`, `home end`, `ctrl+a ctrl+e`, `backspace`, `delete`, `ctrl+w` and
`ctrl+u` edit, `tab` does nothing (except where noted), `↑ ↓` move through the
rows and `pgup pgdn` move a page. A pasted block fills the field with its first
line and never acts. Click a row to select it and click it again to act.
The footer of the box names what `enter` will do.

**Jump (`ctrl+k`).** A filter over your conversations (rooms by name, people by
handle or display name, in any case) and over people you have no direct chat
with yet, which are marked `— new chat`. `enter` opens the selected
conversation, or starts the direct message with the selected person.

**New conversation (`ctrl+n`).** First choose `direct message`, `new room` or
`join an open room` (`↑ ↓`, `enter`; `k j home end` also move here, as there
is no field). `esc` goes back to the choices, and from the choices closes.
- *Direct message:* type part of a handle. `enter` opens the direct message with
  the selected person, and its footer says who (`⏎ message sam`). With an
  empty field, or when nothing matches, `enter` does nothing.
- *New room:* type a name (lowercase letters, digits, `-` and `_`, starting with
  a letter or digit, at most 32 characters; a bad name is explained as you
  type). `tab` switches between private (by invitation) and open (anyone on the
  server can join). `enter` creates the room; with an empty field it does
  nothing.
- *Join an open room:* lists open rooms you are not in, with member counts.
  `enter` joins the selected one. With none it says `no open rooms to join`.

**Search (`/`).** From the chat list or the thread, not from the input. Type
words; after a short pause it searches your local history for messages that
contain every word, newest first, up to 50. Each result reads
`conversation · author · text` with the first matching word marked. `enter`
opens the conversation scrolled to that message, which is marked for two
seconds. A result in a conversation you have left is marked `(left)`; its
history is still searchable, but `enter` on it shows `you are no longer in this
conversation` and opens nothing.

**Room panel (`ctrl+o`).** In a room, shows its members (you marked `(you)`,
agents `[agent]`), whether the room is open or private, and who created it. In
a direct message it says `not a room`. `↑ ↓` or `k j home end` move.
- `l` leaves the room, after y/n. Anyone can.
- For the room's creator and for admins only: `a` invites someone (a picker of
  people not in the room; `enter` invites the selected person, and its footer
  names them), `x` removes the selected member after y/n, and `n` renames the
  room (type a name, `enter` saves). The rows `invite someone`, `rename the
  room` and `leave the room` at the end of the list do the same with `enter`;
  `enter` on a member offers to remove them. Others do not see these keys.
- `esc` goes back from the invite and rename forms to the members, and from
  the members closes the panel.

**Help (`?`).** The whole key table, grouped as above, from anywhere outside a
text field. `↑ ↓ pgup pgdn` scroll it; `?` or `esc` closes it.

### Quitting

`q` (outside the input) or `ctrl+c` quits. With unsent drafts or an edit in
progress, the first press shows a warning for three seconds, and the same key again
within those three seconds quits. When the draft is in the conversation you have open, the
warning reads `q again to quit — unsent drafts will be lost` (or `ctrl+c
again to quit`, whichever key you pressed); when it is in another
conversation, it names it, for example `unsent draft in #platform — q again to
quit`. The background process keeps running.

For the terminal-specific details of paste and timing, see
[terminals and paste](terminals-and-paste.md). For notifications, see
[notifications](notifications.md).

## The config file

`config.toml` is in the config directory: `~/.config/termchat/` on Linux and
`~/Library/Application Support/termchat/` on macOS, or `$TERMCHAT_CONFIG_DIR`.
`join` writes it with every key set. Edit the existing keys; a second copy of
a table such as `[cli]` makes the file invalid, and a file that does not parse
stops the background process from starting. A missing file means the defaults.

```toml
mark_read = true        # opening or reading a conversation in the interface marks it read
splash = true           # show the opening screen
paste_gap_ms = 25       # milliseconds; 0 to 200; see terminals-and-paste.md

[[account]]             # written by `termchat join`
  name = "bob"
  server = "chat.internal:7420"
  handle = "bob"

[cli]
  send = "allow"        # "allow" or "deny": whether `termchat send` may send as you

[notify]
  terminal = true       # route 1: terminal escape sequences
  os = true             # route 2: operating system notification
  osc = "auto"          # "auto", "9", "99", "777" or "off"
  hook = ""             # route 3: a command to run for each notification

[rooms]                 # per-conversation notification mode
  r_1d6c074850726d7d = "all"    # "all", "mentions" (the default) or "muted"
```

| Key | Default | Meaning |
|---|---|---|
| `mark_read` | `true` | The interface marks a conversation read when you open it or read new messages at the bottom. Off: it never does. |
| `splash` | `true` | Show the opening screen. |
| `paste_gap_ms` | `25` | Wait after `enter` for more pasted input. Values outside 0 to 200 are clamped. `0` sends at once. |
| `cli.send` | `"allow"` | `"deny"` makes `termchat send` fail with exit code 6. |
| `notify.terminal` | `true` | Let the interface raise notifications with terminal escape sequences. |
| `notify.os` | `true` | Post through the operating system when the terminal route does not apply. |
| `notify.osc` | `"auto"` | Which escape sequence to use; `"off"` disables. |
| `notify.hook` | `""` | Command run for each notification. |
| `rooms.<id>` | none, meaning `mentions` | Notification mode for one room. Find a room's id with `termchat chats --json`. |
| `account` | none | The joined server. One account is supported. |

Notifications and their keys are explained in
[notifications](notifications.md).

## Directories

| Directory | Default | Override |
|---|---|---|
| Config | `~/.config/termchat` (Linux), `~/Library/Application Support/termchat` (macOS) | `TERMCHAT_CONFIG_DIR` |
| Data | `$XDG_DATA_HOME/termchat`, else `~/.local/share/termchat` | `TERMCHAT_DATA_DIR` |
| Runtime | `$XDG_RUNTIME_DIR/termchat`, else `<data>/run` | `TERMCHAT_RUNTIME_DIR` |

Keep the runtime path short: the Unix socket path is limited to about 100
bytes, and the background process refuses to start with a longer one.
