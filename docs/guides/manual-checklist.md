# Manual checklist before a release

Automated tests cover delivery, catch-up, duplicate protection, edits, read
sync, key removal, the interface's model and the command line. These checks
cover what they cannot: real terminals, real operating-system notifications,
and a second machine. Use the release candidate's binary, and record the
terminal, operating system and result of each item in the release's pull
request.

## 1. Build and install

- [ ] `make lint test build` passes, and `make release-snapshot` builds five
      archives (darwin and linux on amd64 and arm64, windows amd64).
- [ ] `./termchat --version` and the opening screen show the version, not `dev`
      (a snapshot or tagged build, not a plain `go build`).
- [ ] `brew install jpe0824/tap/termchat` then `termchat --version` (after the
      first release), and `go install github.com/jpe0824/termchat/cmd/termchat@latest`.

## 2. First-run walkthrough: server setup and a second machine

Use two machines on the same private network: one runs the server, and both
run clients. Start with no termchat data on either.

- [ ] Server machine: `termchat server init --data <dir> --handle <you>` prints
      an invite code. `termchat serve --data <dir>` prints `listening on ...` and a
      host key fingerprint.
- [ ] Client on the server machine: `termchat join <address>:7420 --invite <code>`
      succeeds, prints `server key SHA256:...` equal to the server's fingerprint,
      and `termchat` opens the interface.
- [ ] `termchat server invite <second-handle>` (or `termchat invite` from the
      first client), then on the **second machine**: `termchat join <address>:7420
      --invite <code>` and `termchat`. The first client sees no host-key prompt and
      the second sees a connected state.
- [ ] A direct message and a room message each arrive on the other machine
      within a second, in both directions, with the interface open on both.
- [ ] Stop the server, send a message from a client (it shows `sending…`), start
      the server, and the message is delivered once.
- [ ] `termchat server key remove` on the second machine's key: that client
      shows `not authorised` rather than retrying in a loop. A new invite for the
      same handle and `termchat join` on that machine bring it back.
- [ ] `termchat server backup` then `restore` into an empty directory, serve
      from it, and the clients reconnect without a host-key warning.
- [ ] The server guide's systemd and Docker steps work on a clean machine.

## 3. Notification delivery

Follow [notifications](notifications.md#testing-notifications-in-your-terminal).
For each terminal, with the interface open on a different conversation, receive
a direct message and a mention in a room, and confirm a system notification
appears, and that closing the interface still produces an OS notification.

- [ ] **Herdr.** This also settles whether Herdr accepts the kitty-style
      sequence (OSC 99). If it does not, record which `notify.osc` value
      works and update the notifications guide and `DetectOSC` for `HC_TERMINAL_ID`.
- [ ] A second terminal that uses OSC 9 (iTerm2, WezTerm or Ghostty).
- [ ] A third terminal of a different kind (kitty, a VTE terminal such as GNOME
      Terminal, or Terminal.app, which uses the OS route).
- [ ] Over SSH to a Linux machine running its own background process:
      the terminal sequence reaches the local desktop.
- [ ] The hook: `hook = 'touch /tmp/termchat-hook-ran'` creates the file.
- [ ] Not notified: your own message, a message already read on another
      device, the conversation on screen in a focused window, a room in `muted`
      mode. One notification per conversation after the background process
      reconnects. Nothing on a new device's first sync.

## 4. Layouts

Resize a real terminal window through each width, with a long room history and
a draft in the input.

- [ ] 120 columns: list, thread and members panel.
- [ ] 90: list and thread; the room panel opens as an overlay.
- [ ] 50: thread only; `esc` or a click on `‹ chats` shows the list.
- [ ] 30: tiny, author on its own line, timestamps gone.
- [ ] Very short window (under 12 rows): the footer, then the top bar, disappear.
- [ ] Resizing keeps the draft, the selection and the scroll position.
- [ ] The mouse: click a conversation, click into the input, wheel scrolls the
      thread, click a failed message to retry it.
- [ ] Copy another person's message with `c` and with a second click, in a
      plain terminal and inside tmux, and paste it elsewhere: the text only.

### Overlays

For each of jump (`ctrl+k`), new conversation (`ctrl+n`: direct message, new
room open and private, join an open room), search (`/`) and the room panel
(`ctrl+o`: members, leave, and as creator or admin invite, remove, rename), at
widths 120, 90, 50 and 30:

- [ ] It opens, shows its footer, acts on `enter` (or its action key), and
      closes with `esc` and with a click outside.
- [ ] A picker with an empty field does nothing on `enter`, and its footer
      names who `enter` will act on once a row is selected.
- [ ] Search opens a result scrolled to its message, which is marked briefly. A
      result in a room you have left is marked `(left)` and cannot be opened.
- [ ] A non-creator, non-admin sees no invite, remove or rename in the room panel.
- [ ] `?` lists every key, and it matches the footer of the focus you opened it from.
- [ ] Paste a line into each field (with and without bracketed paste): it fills
      the field and nothing opens, creates or joins.
- [ ] A notification appears while the interface is open in a terminal that
      supports it, and falls back to the operating-system notification with
      `notify.osc = "off"`.

## 5. Paste

See [terminals and paste](terminals-and-paste.md).

- [ ] A terminal **with** bracketed paste: paste five lines; they land as one
      multi-line draft and nothing is sent until `enter`. Paste text containing
      tabs and escape characters; the draft contains no escape sequences.
- [ ] A terminal **without** bracketed paste (or tmux with it off, or
      `printf '\e[?2004l'` first): paste three lines; they land as one draft.
      Paste a line ending in a line break; it sends. A single pasted `q` on the chat
      list acts as `q`. Paste text with `ctrl+u` in it and watch it clear the draft.
- [ ] `paste_gap_ms = 0` and `paste_gap_ms = 200`: behaviour changes as the
      guide says.
- [ ] Quit (`q`, then `q` again) with an unsent draft: the warning appears and
      the second press quits.

## 6. Upgrade with a running background process

- [ ] With the old version's background process running and the interface
      closed, install the new binary. A client command (`termchat status`) still
      works against the old process.
- [ ] `termchat daemon stop`, then `termchat status`: a new background process
      starts from the new binary and reconnects.
- [ ] With the interface open during the upgrade, if the versions differ it
      shows `background process is a different version — run: termchat daemon stop`.
- [ ] Messages sent just before the stop are delivered exactly once afterwards.
