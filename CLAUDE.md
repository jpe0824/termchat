# termchat

termchat is a chat-only tool for the terminal: a self-hosted server and a client that run from one Go binary (module `github.com/jpe0824/termchat`, MIT). It is not an AI chat interface (AI tools reach it through the CLI, as a person or as their own agent account), and it has no calls or screen sharing. One server is one team on one machine: no federation, no clustering. It is built for a private network; do not expose a server to the internet.

## Commands

```
make lint test build        # what CI runs (also: GOOS=windows go build ./...)
CGO_ENABLED=0 go test ./internal/server/...    # one package
CGO_ENABLED=0 go test -race ./internal/daemon/...
go test ./internal/tui -update                 # rewrite golden frames; review the diff
termchat serve --data /tmp/tcs                 # server, listens on :7420 (--addr, --retention)
termchat server init|invite|user|key|room|backup|restore --data /tmp/tcs   # admins: server user admin <handle>
termchat join <host:port> --invite <code>      # client; then status, chats, read, send, wait, ...
```

`make lint` is `go vet ./...`. `make test` and `make build` set `CGO_ENABLED=0`; the CLI end-to-end tests build the real binary and take about a minute. `termchat` with no arguments opens the TUI on a terminal and prints help otherwise. Server commands take `--data <dir>` or `TERMCHAT_SERVER_DATA`; there is no default, so a server never starts on a directory by accident.

Two people on one machine: give each their own three directories, with a short runtime path.

```
export TERMCHAT_CONFIG_DIR=/tmp/tc-a/c TERMCHAT_DATA_DIR=/tmp/tc-a/d TERMCHAT_RUNTIME_DIR=/tmp/tc-a/r
```

Use `/tmp/tc-b/...` in a second shell. Stop each person's background process with `termchat daemon stop` when done.

## Architecture

```
 TUI (internal/tui)   CLI (internal/cli)
        \                /       Unix socket, ipc: newline-delimited JSON
         v              v
     background process (internal/daemon): store, outbox, notifications
                  |  connector.Connector
     internal/connector/termchat  --- SSH channel "termchat", NDJSON (proto) --->  termchat serve
```

- The server (`internal/server`) keeps an append-only event log per conversation, numbered by the server, in SQLite (pure Go). It speaks newline-delimited JSON (`internal/proto`) over an SSH channel and identifies each device by its public key. Admin commands run on the server machine against its data directory.
- On the client, one background process per user (`termchat daemon run`, one per user via a lock file) owns the server connection, a local SQLite copy (`internal/store`), the outbox and notifications. The CLI and TUI are thin clients of it over the socket. It starts itself on first use and keeps running after they exit, so notifications arrive with the TUI closed. It is not supported on Windows (which must still compile).
- Package jobs: `model` neutral types and name rules; `sanitize` terminal-safe text; `paths` and `config` per-user directories and `config.toml`; `connector` the network-neutral interface (`fake` is a scripted implementation, `termchat` the real one); `ipc` socket protocol, client, server; `notify` notification policy, OSC and OS and hook routes; `testutil` in-process server harness; `cli` the cobra command tree; `cmd/termchat` only calls `cli`.

## Rules that must hold

Import boundaries (the reason: each layer stays replaceable, and a client can never bypass the background process):
- Only `internal/server` and `internal/connector/termchat` import `internal/proto` (`testutil` does too, for tests).
- `internal/store` imports only `internal/model`. `internal/proto` and `internal/connector` import only `model`.
- `internal/ipc` and `internal/daemon` never import `proto` or `server`.
- The CLI's client commands and the TUI talk to `internal/ipc` and `internal/model`, plus `sanitize`, `config`, `paths`, and the OSC helpers in `notify` for the TUI. They never open the store or call a connector. `ipc` re-exports the store and connector types they display (`ipc.Message`, `ipc.ConvView`, `ipc.RoomOp`); use those aliases. Only the server-admin CLI files (`serve.go`, `server_admin.go`) may import `internal/server`.
- No cgo anywhere; SQLite is `modernc.org/sqlite`. `CGO_ENABLED=0` must build and test. macOS and Linux are supported; Windows must compile.
- `internal/archtest` enforces the rules above from every file's imports (CLI tests may import `server`; no test outside `server`, `connector/termchat` and `testutil` imports `proto`). Change its table only when the rule itself changes.

Message guarantees:
- A message is never dropped silently and never delivered twice. Ordering is by server event number, never a clock.
- A send is written to the outbox first and retried with the same message id, which the server dedups. An outbox item waiting longer than 10 minutes fails with reason "expired" instead of going out late.
- An unknown outcome (request sent, no answer: `connector.ErrUnknownOutcome`) is settled by syncing, never guessed. Only `Send` is safe to repeat blindly.
- A gap in a conversation's held log (live event past missing ones, a room change's own events, a failed store write) is queued with `checkGap` and synced by `supervise`, never by the drain goroutine.
- The server never parses a message body (it is an opaque `model.Body`); mentions travel beside it as handles. There is no end-to-end encryption: whoever runs the server can read messages.

Privacy on the server (`internal/server/authz.go`):
- A conversation the caller cannot see answers exactly like one that does not exist (`not_found`). Load conversations only through `visibleConv` or `memberConv`.
- Managing a room needs current membership; admins are the exception. Authorisation reads the caller as stored now, so a removed or demoted person loses access on the next request.
- A removed handle is tombstoned and can never be registered again. The last admin cannot be removed or demoted.
- An invite records its kind: `person` (creates the handle; refused once it exists) or `device` (adds a key to an existing handle; refused once it is gone). `RedeemInvite` checks the kind inside the redeeming transaction; in production code it is the only path that adds a key (`Store.AddKey` serves the test harness).

Terminal safety:
- Every string from another person or the server passes through `internal/sanitize` (`Text`, `Line`) before it is drawn or printed.
- Message text reaches notification commands only as an argument or an environment variable (`TERMCHAT_TEXT` etc.), never through a shell string. It reaches escape sequences only after control characters and `;` are stripped (`notify.OSC`).
- Never log message text.

Concurrency in the background process:
- The goroutine draining connector updates (`ledger.drain`) never calls the connector and never blocks on a process, a timer or a subscriber. The connector stops reading the network when the consumer lags, so a request made from the drain goroutine can deadlock.
- A slow subscriber is dropped (its queue is `queueLen` frames), never waited for; it reconnects and re-reads.

TUI and paste safety:
- The update loop never blocks. Every backend call is a `tea.Cmd` returning a message tagged so stale results are dropped. Events from the background process are hints to re-read, not data.
- Terminals without bracketed paste deliver a paste as individual key events. Keys are therefore classed as immediate (navigation only, so held-down repeat works), guarded (act only as a lone press, see `guarded` in `internal/tui/guard.go`) or deferred (exits from a text field, held for the paste gap). The gap is `paste_gap_ms` (default 25, capped at 200, negative or 0 disables).
- With a text field focused, printable keys type and `tab` inserts a tab. Outside a text field only commands act, and other keys are dropped or leave a hint. A new key is added to the one table in `internal/tui/keytable.go` (class, action, help text), which drives dispatch, the help overlay and footers. Any new key handling must follow this, and the paste matrix test (`internal/tui/matrix_test.go`, plus `overlay_paste_test.go` for overlays) must cover it.

## Testing

- Write the test first. Server and client share one repo so tests can use the real thing: `internal/testutil` starts an in-process server (`StartServer`, `NewPerson`); the CLI tests (`internal/cli/e2e_*_test.go`) run the real built binary with one private config/data/runtime directory set per simulated person; `internal/connector/fake` backs the daemon tests, so no server is needed there.
- TUI tests are model-level: key and mouse events in, state and rendered frame out, with golden frames in `internal/tui/testdata`. The backend is a fake (`fakeBackend` in `helpers_test.go`).
- Use injected clocks (`daemon.Clock`, `tui.Options.Now`), not sleeps. Where a real process is involved, poll with the `eventually` helper and a deadline.
- Assert specifically: the exact error (`errors.Is`, the code, the message), the exact frame or value. Never accept "any error", and never treat a timeout as success.
- Tests must leave no process behind: the e2e `TestMain` fails the run if a background process outlives it. Run `termchat daemon stop` after manual trials.
- Do not start the TUI in an automated session; drive the model in tests.

## Conventions

- Comments state constraints the code cannot express. No narrating a change, no references to a ticket, plan or review.
- Keep source files focused, roughly under 350 to 450 lines; split by responsibility when one grows past that.
- Commits are conventional: `feat:`, `fix:`, `refactor:`, `test:`.
- Never commit directly to `main`. Work on a branch named `type/description`.
- Committed durable docs live in `docs/termchat.md`, `docs/guides/`, `docs/notes/`, `docs/memory/context.md`. `docs/plans/`, `docs/specs/`, `docs/superpowers/`, `docs/memory/active/` and `docs/memory/archive/` are local working files, gitignored, never committed. When a change makes a guide stale, updating it is part of that change.

## Gotchas

- Unix socket paths are limited (103 bytes on macOS, 107 on Linux). The daemon refuses to start with a longer path and names `TERMCHAT_RUNTIME_DIR`. Tests that build directories use `/tmp`, not `t.TempDir()`, for that reason.
- Any CLI command that needs the background process starts it (`ipc.Dial`); the TUI's reconnects use `ipc.DialRunning`, so a process the user stopped stays stopped. Settings change live through `termchat config set` and `termchat room notify`; the background process re-reads them at once. A hand edit to `config.toml` applies at the next restart or the next settings change. From the CLI, `cli.send` can be set to `deny` but not `allow`, and `notify.hook` cannot be changed (exit 7). Its log is `daemon.log` in the data directory.
- Directories resolve from `TERMCHAT_*_DIR` first. Config then uses Go's `os.UserConfigDir`: `$XDG_CONFIG_HOME` or `~/.config` on Linux, but always `~/Library/Application Support/termchat` on macOS, where `XDG_CONFIG_HOME` is ignored. Data uses `$XDG_DATA_HOME`, then `~/.local/share/termchat`, on both. The runtime directory uses `$XDG_RUNTIME_DIR`, then falls back to `<data>/run`.
- Pinned values: default port 7420 (`internal/cli/serve.go`, `connector/termchat/keys.go`); message limit 8192 bytes (`model.MaxTextBytes`, server `defaultMaxBody`); handle and room name pattern `^[a-z0-9][a-z0-9_-]{0,31}$` (`model.ValidName`); server rate limits and auth-failure cap in `internal/server/listener.go`; layout breakpoints 36, 70 and 110 columns (`tui.LayoutMode` in `internal/tui/layout.go`); `paste_gap_ms` in `internal/config`.
- The client never uses the user's own SSH keys: `join` generates a dedicated unencrypted key (mode 0600) in the data directory, because the background process must reconnect unattended. The server host key is pinned on first connect and a later change is refused.
- `join` replaces the configured account only for the same server in `not_authorised` or `host_key_changed`, or when the account could not be brought up. Whether the old store is kept is decided only in `daemon.bringUp`, from the server key fingerprint the store recorded (`Store.ServerKey`): a different pin moves the store to `<data>/set-aside/`, since a rebuilt server's event numbers restart under the same conversation ids. `join --server-key SHA256:...` checks the host key before the invite is sent.
- The CLI's `read` and `wait` do not move the read position; only the TUI and `mark-read` do, so an agent reading as a user never hides messages from them. `cli.send = deny` in config disables CLI sends.
- Output is a table on a terminal and JSON when piped or with `--json`. Exit codes are fixed (`internal/cli/exit.go`); keep them stable for agents.
- The module needs a recent Go (see `go.mod`); CI reads the version from it.
