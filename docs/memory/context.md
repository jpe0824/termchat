# termchat: long-lived project context

What a contributor needs to know that the code does not say. For how the code
is organised see [architecture](../termchat.md).

## What it is

A chat-only terminal client with its own self-hosted server, in one Go binary.
The first use is a dev team that works in terminal tools (Herdr in particular)
and wants chat there, reachable by AI agents through a command line.

## Decisions and the reasons

- **Its own server, not Teams or Matrix, for the first version.** Teams
  through Microsoft Graph needs an app registration or admin consent in an
  employer's tenant, which a personal open-source project must not depend on.
  Matrix has existing apps but a large protocol, hard encryption and a heavier
  server, and good terminal clients already exist. IRC has no edits and weak
  room for growth. A small protocol of our own is built for live delivery and
  for agents, and keeps a team's chat on its own machine. The cost is that
  termchat is the only client and the project owns the security of a server.
- **SSH protocol with a key per device.** It gives encryption and identity
  without certificates, a domain name or passwords. `join` generates a
  dedicated key (not the user's own SSH key, because the background process
  must reconnect unattended, which a passphrase, hardware key or agent would
  break) and stores it with mode 0600. The client pins the server's host key on
  first connect and refuses a change. Nothing in the messages depends on SSH,
  so a WebSocket listener could be added later.
- **A background process per user.** The interface and the command line must
  see the same state, one connection per user is simpler than several, and
  notifications must work with the interface closed.
- **Go with Bubble Tea.** One static binary, fast builds, an application
  framework for the interface, cross-compilation, and the same binary as client
  and server. SQLite uses a pure-Go driver so `CGO_ENABLED=0` holds everywhere.
  The input component is our own (`internal/tui/input.go`) so the cursor and
  wrapping use the same width rules as the thread, and so paste is controllable.
- **An append-only event log per conversation.** One shape serves live
  delivery, catch-up, edits and several devices. Messages carry client-chosen
  ids, so a retry is never posted twice.
- **No end-to-end encryption yet.** Traffic is encrypted in transit and the
  server stores text readably. The server treats the body as an opaque payload
  with a format tag and carries mentions in a separate list, so adding
  end-to-end encryption later means a new payload format and key exchange, not
  a new protocol. It would change two behaviours this version has: a new member
  of a room reading earlier history, and a new device receiving history without
  help from an existing one. At rest, the answer today is disk encryption and
  passphrase-encrypted backups.
- **Private network only.** The first deployment sits behind a VPN. The server
  has per-connection rate limits, a frame size limit, a message size limit
  (8192 bytes), a cap on failed authentications per address, and allows nothing
  before a key is recognised except redeeming an invite. Hardening and abuse
  testing for a public address are not done, and the docs say not to expose it.
- **Notifications in three layers** (terminal sequences, the operating system,
  a hook) because no single route works in every terminal and on every machine.
- **Read position is per person, moved only by the interface and `mark-read`.**
  `read`, `wait` and `search` never move it, so an agent acting as you never
  hides a message from you.
- **The interface treats keys that arrive close together as a paste** when the
  terminal has no bracketed paste, because the key decoder cannot tell a pasted
  line break from `enter`. That costs a short wait before sends; see
  [terminals and paste](../guides/terminals-and-paste.md).
- **Homebrew formula, not cask.** GoReleaser's `brews` section is deprecated
  since 2.10 in favour of `homebrew_casks`, but casks are macOS-only, have no
  `test` block and no license field, and an unsigned binary in a cask is
  quarantined by macOS Gatekeeper. A formula installs on macOS and Linux and
  runs `termchat --version` as its test. `goreleaser check` exits 1 on the
  deprecation warning while `goreleaser release` works and only warns, so CI
  does not run `check`. When `brews` is removed, the options are: move to
  `homebrew_casks` (macOS only, no `test`, a post-install hook to clear the
  quarantine flag or signed and notarised binaries, and Linux users fall back
  to the release archive or `go install`); or stop using GoReleaser's Homebrew
  support and keep `Formula/termchat.rb` in the tap as a hand-maintained file
  updated from `checksums.txt` by a small release step.
- **The formula is pushed to the tap with a deploy key,** not a personal access
  token: a read-write deploy key on `jpe0824/homebrew-tap`, whose private half
  is the Actions secret `HOMEBREW_TAP_DEPLOY_KEY` in this repository. A deploy
  key is scoped to that one repository and belongs to no person's account.
  GoReleaser pushes over SSH (`brews[].repository.git`) and skips the push when
  the variable is empty, so snapshots and forks still build.

## Known limits

- **One account.** The data model and the `--account` flag allow several, but
  the background process and interface handle one server.
- **No background process on Windows.** The Windows binary can run the server;
  the client commands need the background process, which refuses to start there.
- **The background process reads `config.toml` at start.** Notification and
  `cli.send` changes need `termchat daemon stop`. A config file that does not
  parse stops the process from starting, and the client reports only that the
  socket cannot be reached.
- **A server's host key change needs a manual step on every client**
  (delete its line from `known_servers`, then `join` again), by design. `join`
  replaces the configured account only when it names the same server and the
  connection is `not_authorised` or `host_key_changed`.
- **Gaps in a conversation's log are repaired by a sync from the supervise
  goroutine** (`internal/daemon/gaps.go`): a live event past a gap, a room
  change's own events (the server never pushes them to the requesting device)
  and a failed store write all queue one. A removal by `server user remove`
  in another process is therefore seen by connected members at the next event
  in that room, not at once.
- **`wait` is not resumable inside a call.** If the background process stops
  during `wait`, it exits 1; callers rerun it with `--since`. A stalled store
  call inside the background process can hold `wait` for up to the socket
  request timeout (90 seconds).
- **Notification retry state is in memory.** A message the background process
  could not evaluate (for example its conversation was not stored yet) is retried
  a few times, and forgotten if the process restarts.
- **Open rooms you have not joined** appear in `chats --all` and `room join`
  from a cache refreshed at connect, on a `room join` miss, and at most every
  five seconds on `chats`.
- **The interface has no way to set a room's notification mode;** it is a
  `config.toml` edit.
- **`termchat server room list` lists rooms only,** not direct messages. People
  and keys can be removed only from the server machine's command line.
- **A listener failing after start does not stop the server;** it retries
  accepting and logs warnings.
- **Unix socket path length** (about 100 bytes) limits the runtime directory.
- **Not built:** typing indicators, presence, threads, reactions, rich
  formatting, federation, clustering. Messages are plain UTF-8; URLs show as
  typed.
- **The Linux `gdbus` notification route is untested** on real systems, and
  there are no notifications on Windows because the background process does not
  run there. Whether Herdr honours OSC 99 is unconfirmed.
- **No login item** starts the background process at boot; it starts on demand.

## Later projects

- **Attachments:** message parts the server stores and the client fetches only
  when asked. Opt-in per user.
- **Teams and other networks (Slack, Discord):** more implementations of
  `internal/connector`, with nothing above the boundary changing. Until a Teams
  connector exists, colleagues who are not on termchat stay in Teams.
- **Phones and web:** what is in place is the event log, per-person read
  position, several devices per person and transport-neutral messages. Missing
  are a second listener a browser can reach, a credential other than an SSH key,
  and push delivery.
- **Several accounts** in the background process and interface.
- **End-to-end encryption and a hardened public listener**, as above.
