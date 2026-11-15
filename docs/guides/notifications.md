# Notifications

termchat tells you about new messages through up to three routes, each
switchable in `config.toml`. The background process decides what is worth a
notification and routes it, so notifications arrive even when the interface is
closed.

## The three routes

| Route | Raised by | Used when |
|---|---|---|
| 1. Terminal escape sequence | the interface, in your terminal | The interface is open, `notify.terminal = true`, and your terminal is one termchat knows (or `notify.osc` names a sequence). The terminal turns the sequence into a system notification. It works over SSH. |
| 2. Operating system | the background process | Route 1 did not apply (no interface open, or it cannot raise sequences, or `notify.terminal = false`) and `notify.os = true`. |
| 3. Hook command | the background process | `notify.hook` is set. It runs for every notification, in addition to route 1 or 2. |

Routes 1 and 2 are alternatives: a notification is not shown twice. The hook is
independent of both.

How route 1 is chosen. When the interface starts, it works out which escape
sequence your terminal understands (from `notify.osc` and the environment, see
the table below). If it found one, it tells the background process that it can
show notifications; if `notify.osc` is `off` or the terminal is unknown, it says
it cannot. For each notification, the background process checks `notify.terminal`
and whether an interface that can show it is attached. If so, it sends the
notification to that interface, which writes the escape sequence followed by a
bell to the terminal, and the operating-system route is not used. Otherwise,
with `notify.os` on, the background process posts it through the operating
system. The interface decides at start: changing `notify.osc` needs the
interface restarted, and `notify.terminal`, `notify.os` and `notify.hook` need
`termchat daemon stop`.

Route 2 uses `osascript` on macOS and `notify-send` (or `gdbus` when
`notify-send` is missing) on Linux. The `gdbus` path has not been tried on a
real system.

**Windows has no notifications in this version.** The code contains a
PowerShell toast route and a `cmd /C` hook runner, but every route is driven
by the background process, which does not run on Windows (see
[getting started](getting-started.md#the-background-process)), so none of them
can fire there. On macOS a
command-line program cannot post through Apple's notification centre itself, so
the notification comes from `osascript`; you may need to allow notifications
for it in System Settings.

## What notifies

A message notifies when all of these hold:

- It is a **direct message**, or it is in a **room** whose mode lets it
  through: `all` notifies for every message, `mentions` (the default) only for
  messages that mention you with `@handle`, `muted` never.
- It is not your own, and not deleted.
- It is **unread**: it is after your read position, which another device may
  have moved. Reading it on your phone or laptop first silences it here.
- It is **not in the conversation on screen**: an interface with that
  conversation open in a focused terminal window counts as watching it.
  See [how focus is detected](#how-focus-is-detected).

Not notified, ever: edits, deletions, membership changes, and history that is
being filled in.

After the background process reconnects (or restarts), it raises **at most one
notification per conversation**, for the newest message that qualifies, not one
per message missed. A **new device's first sync is silent**: when a device has
no conversations stored yet, nothing it downloads notifies. The notification
text is cut to 200 characters, with control characters removed.

Set a room's mode in `config.toml` under `[rooms]`, keyed by the room's id
(`termchat chats --json` lists ids):

```toml
[rooms]
  r_1d6c074850726d7d = "all"
  r_a1b2c3d4e5f60718 = "muted"
```

The mode does not apply to direct messages. The background process reads the
config at start: run `termchat daemon stop` after editing.

## How focus is detected

The interface turns on the terminal's focus reporting, so the terminal says
when its window gains or loses focus. Until the first such report arrives,
focus is **unknown**, and the interface counts as **not focused**: messages in
the conversation on screen notify like any other. Some terminals and
multiplexers never report focus (Herdr panes, and tmux without
`set -g focus-events on`), so focus stays unknown for the whole session there.

Focus also decides when a new message in the open conversation is marked read
(with `mark_read` on, and the thread at its newest message):

- **Focused:** at once.
- **Not focused:** when the window gets focus back.
- **Unknown:** on your next key press, paste, click or scroll in the
  interface. Input from before the message arrived does not count, so a
  message that arrives while you are away stays unread.

Opening a conversation marks it read in all three cases.

## Config keys

```toml
[notify]
  terminal = true    # route 1
  os = true          # route 2
  osc = "auto"       # "auto", "9", "99", "777" or "off"
  hook = ""          # route 3: a shell command
```

On a machine you reach over SSH, the background process runs on that machine
too, so only the terminal sequences (through your SSH session) and the hook
apply usefully. Set `notify.os = false` there.

## The hook

`hook` is run through `sh -c` with these environment
variables, and nothing else is passed:

| Variable | Value |
|---|---|
| `TERMCHAT_CONV` | The conversation's id. |
| `TERMCHAT_CONV_NAME` | The room name, or the other person's handle for a direct message. |
| `TERMCHAT_SENDER` | The sender's handle. |
| `TERMCHAT_TEXT` | The message text, at most 200 characters, control characters removed. |
| `TERMCHAT_MENTION` | `1` if the message mentions you, else `0`. |
| `TERMCHAT_DM` | `1` for a direct message, else `0`. |

```toml
[notify]
  hook = 'printf "%s %s: %s\n" "$(date +%H:%M)" "$TERMCHAT_SENDER" "$TERMCHAT_TEXT" >> "$HOME/termchat-notifications.log"'
```

Always quote the variables: they hold other people's text. A hook has stdin
closed and its output discarded; it is stopped after 5 seconds, and at most four
run at once (more are dropped and noted in `daemon.log`). Hooks are how
termchat feeds other tools, such as an AI CLI's status line or a phone push
service.

## Terminals and the sequence each uses

With `notify.osc = "auto"` the interface picks a sequence from environment
variables:

| Terminal | Detected by | Sequence |
|---|---|---|
| Herdr (assumed) | `HC_TERMINAL_ID` set | OSC 99 (kitty style) |
| kitty | `KITTY_WINDOW_ID` set | OSC 99 |
| iTerm2 | `TERM_PROGRAM=iTerm.app` | OSC 9 |
| WezTerm | `TERM_PROGRAM=WezTerm` | OSC 9 |
| Ghostty | `TERM_PROGRAM=ghostty` | OSC 9 |
| VTE terminals (GNOME Terminal, Tilix, ...) | `VTE_VERSION` set | OSC 777 |
| Anything else | none | no sequence; route 2 is used |

Terminals also need notifications turned on in their own settings, and some
only notify while the terminal window is in the background.

**Herdr is assumed to accept the kitty-style sequence (OSC 99). This has not
been confirmed.** The assumption comes from Herdr's binaries mentioning
`kitty_desktop_notification`. If notifications do not appear in Herdr, set
`osc` to another value, and test again as below:

```toml
[notify]
  osc = "9"       # or "777"; or "off" to fall back to the OS route
```

## Testing notifications in your terminal

Send the sequence by hand. If a notification appears, the terminal supports
it and `notify.osc` should be that number.

```sh
printf '\e]9;termchat: hello\a'                                   # OSC 9
printf '\e]777;notify;termchat;hello\a'                            # OSC 777
printf '\e]99;i=tc:d=0;termchat\e\\\e]99;i=tc:d=1:p=body;hello\e\\'  # OSC 99
```

Then test termchat itself. Open the interface on a conversation other than
the one you will message, and send a direct message to yourself from another
account or device (`termchat send @you "test"` from the other one). If
nothing appears, check in this order:

1. `termchat status` shows `connected`.
2. `notify.terminal`, `notify.os` and `notify.osc` are what you expect, and you
   ran `termchat daemon stop` after editing.
3. The message qualifies: a direct message, or a mention in a room with the
   default mode, and you are not looking at that conversation.
4. The hook route: set `hook = 'touch /tmp/termchat-hook-ran'` and look for the
   file.
