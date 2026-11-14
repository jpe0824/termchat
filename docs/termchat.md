# termchat architecture

This is the starting point for a contributor. It describes how the code is
organised and where the important guarantees are enforced. For running and
using termchat, see the guides in `docs/guides/`.

## The four roles

One binary, `termchat`, plays four roles. The command that starts each is in
brackets.

```
      person's machine                                      team's server

 ┌───────────┐   ┌─────────────┐
 │    TUI    │   │ command-line│     both are clients of the
 │ (termchat)│   │   client    │     background process only
 └─────┬─────┘   └──────┬──────┘
       └──── Unix socket ┘      (internal/ipc, newline-delimited JSON)
              │
       ┌──────▼───────────┐   local SQLite store (client.db)
       │ background       │   outbox, read positions, search index
       │ process          │   notifier (OS notification, hook)
       │ (daemon run)     │
       └──────┬───────────┘
              │ connector interface (internal/connector)
       ┌──────▼───────────┐     SSH transport,      ┌──────────────────┐
       │ termchat         │◄───── newline-delimited ►│  termchat serve  │
       │ connector        │       JSON frames        │  SQLite server.db│
       └──────────────────┘                          └──────────────────┘
```

- **Server** (`termchat serve`, administered with `termchat server ...`). One
  process, one SQLite file, one port (default 7420). It authenticates devices
  by SSH public key, keeps an append-only event log per conversation, and
  pushes new events to connected devices.
- **Background process** (`termchat daemon run`, started on demand by any
  client command). One per user, enforced by a lock file in the runtime
  directory. It owns the connection to the server, the local store, the
  outbox and notifications. It keeps running after the clients exit.
- **TUI** (`termchat`, `termchat tui`). A client of the background process
  only. It never opens the database or the network.
- **Command-line client** (`termchat chats`, `read`, `send`, `wait`, ...).
  Another client of the same socket, for scripts and agents.

Why a background process: the TUI and the CLI must see the same state, one
connection per user to the server is simpler than several, and notifications
must work while the TUI is closed.

## What each package owns

| Package | Owns |
|---|---|
| `cmd/termchat` | `main`: runs the command tree and exits with `cli.ExitCode`. |
| `internal/cli` | The command tree (cobra): flags, output (table or JSON), exit codes, `wait`. `cli.Version` is the build version, set with `-ldflags`. |
| `internal/tui` | The Bubble Tea interface: layout, focus, input, overlays. Talks to the background process through the `Backend` interface. |
| `internal/ipc` | The protocol between the background process and its clients: socket server, `Client`, subscriptions, errors, version handshake. |
| `internal/daemon` | The background process: accounts, sync with the server, the outbox, read marks, notification decisions. |
| `internal/connector` | The network-neutral interface a connector implements, plus `connector/termchat` (SSH client, host-key pinning, device keys) and `connector/fake` (in-memory, for tests). |
| `internal/store` | The client's SQLite database: conversations, events, messages, full-text index, outbox. |
| `internal/server` | The server: SQLite schema, SSH listener, sessions, handlers, authorisation, hub (live fan-out), admin operations, backup and restore. |
| `internal/proto` | The client-server wire protocol: frame types, codec, size limit, error codes. |
| `internal/model` | Network-neutral types (person, conversation, event, message body) and the name and text rules. |
| `internal/notify` | Notification policy (`ShouldNotify`), terminal escape sequences, the OS notifier, the hook runner. |
| `internal/config` | `config.toml` loading and saving, defaults, clamping. |
| `internal/paths` | Config, data and runtime directories and their environment overrides. |
| `internal/sanitize` | Making untrusted text safe to print to a terminal. |
| `internal/testutil` | A harness that starts a real server for end-to-end tests. |

## The event-log model

Each conversation is an append-only log of events numbered by the server:
message posted, message edited, message deleted, member joined or left, room
renamed. One shape covers live delivery, catching up after being offline,
edits, and several devices.

- **Live:** the server publishes each new event to every connected device of
  every member (`internal/server/hub.go`).
- **Catch-up:** on connect the client reports the last event number it holds
  per conversation and the server sends what is missing. History older than the
  first 50 events is fetched by scrolling or by the background backfill, so
  search covers everything and works offline.
- **Exactly once:** the client gives every message its own id. The server
  records `(conversation, msg_id)` with a unique constraint, so a retried send
  is recognised and never posted twice.
- **Edits and deletes** blank the original event's text on the server and in
  every client's store and search index. The event keeps its number.
- **Read position** is kept per person on the server and synced, so reading on
  one device clears unread counts on the others.

The server stores message bodies as an opaque payload with a format tag
(`text/plain`) and carries mentions as a separate list, so the server never
parses message text.

## A message from keystroke to another screen

1. In the TUI, `enter` waits for the paste gap (see
   [terminals and paste](guides/terminals-and-paste.md)), then calls
   `Backend.Send` with the draft.
2. The `ipc` client sends a `send` request over the Unix socket to the
   background process.
3. `daemon.Send` writes an outbox row (state `sending`) to `client.db` before
   any network call and returns the message id. The TUI shows the message
   at once, marked `sending…`.
4. The send loop for that conversation passes the item to the connector, which
   sends a frame over the SSH connection. On a network failure it retries with
   backoff; an item not sent within ten minutes becomes `failed` rather than
   going out late.
5. The server's handler checks authorisation, assigns the next event number in
   the conversation, stores the event and the message id in one transaction,
   and publishes the event.
6. The sender's own device and every other member's device receive the event
   from the server. Each background process applies it to its store, announces
   it to its attached clients, and runs the notification policy.
7. Each attached TUI gets a `message` event on its subscription, re-reads the
   conversation from the background process, and redraws.

## Import boundaries

These hold today and are what keep the layers swappable.

- `tui` imports `ipc`, `model` and `sanitize` only. It never imports `store`,
  `connector`, `daemon` or `server`; `ipc` re-exports the few types it needs.
- `cli` talks to the background process through `ipc`. It reaches further in
  four places: `daemon.Run` and `notify.New` (the hidden `daemon run`
  command), `server` (the `serve` and `server` commands), `tui`, and plain
  types and constants from `connector` and `store` that the `ipc` signatures
  expose.
- `server` imports `model` and `proto` only. It has no client code.
- `connector` imports `model`. Everything above the connector (`daemon`,
  `store`, `ipc`, `tui`, `cli`) uses the neutral model and could run against a
  different network by adding a connector.
- `model`, `sanitize` and `paths` import nothing from the module.

## Where the guarantees are enforced

| Guarantee | Enforced in |
|---|---|
| A sent message is never lost silently | The outbox row is written before any network call (`daemon/outbox.go`); a failed item stays visible with its reason. |
| A message is never delivered twice | The `msg_ids` unique constraint on the server (`server/schema.go`). Nothing resends automatically after a failure the user can see. |
| Events arrive in order per conversation | The server commits and publishes each event while holding that conversation's lock (`server/hub.go`), and assigns the number inside the write transaction (`server/events.go`). |
| Only a known key can do anything | `server/session.go`: before a key is recognised, the only request allowed is redeeming an invite. |
| Admin and room permissions | `server/authz.go` and the handlers; the last admin cannot be removed (`server/people.go`). |
| Rate and size limits | Per-session rate limiter (`server/session.go`), frame limit 1 MiB (`proto`), message text limit 8192 bytes (`model.MaxTextBytes`). |
| The server's identity does not change silently | Host key pinned on first connect in `known_servers`; a different key stops the connection (`connector/termchat/hostkey.go`). |
| Untrusted text cannot drive the terminal | Every string shown goes through `internal/sanitize` (TUI, CLI table output, notifications). |
| Reading as an agent does not mark read | `read`, `wait` and `search` never call `MarkRead`; only the TUI and `mark-read` do (`daemon/api.go`). |
| One background process per user | Lock file `daemon.lock` in the runtime directory (`daemon/lock_unix.go`). |
| Notification rules | `notify.ShouldNotify` plus the routing in `daemon/notify.go`. |

## Where state lives

| What | Where |
|---|---|
| Server database, host key | `<--data>/server.db`, `<--data>/host_key` |
| Client config | `config.toml` in the config directory (`$TERMCHAT_CONFIG_DIR`, else the platform's user config directory plus `termchat`) |
| Client local store, device key, pinned server keys, log | `client.db`, `keys/<host:port>.ed25519`, `known_servers`, `daemon.log` in the data directory (`$TERMCHAT_DATA_DIR`, else `$XDG_DATA_HOME/termchat`, else `~/.local/share/termchat`) |
| Socket and lock | `daemon.sock`, `daemon.lock` in the runtime directory (`$TERMCHAT_RUNTIME_DIR`, else `$XDG_RUNTIME_DIR/termchat`, else `<data>/run`) |

Unix socket paths are length-limited (about 100 bytes). The background process
refuses to start with a longer path and says to set `TERMCHAT_RUNTIME_DIR`.

## Running the tests

```sh
make lint test build          # go vet, go test ./..., build ./termchat
CGO_ENABLED=0 go test ./internal/tui/...      # one package
GOOS=windows go build ./...   # CI also checks that Windows builds
```

- Everything is pure Go (`CGO_ENABLED=0`): SQLite uses a pure-Go driver.
- End-to-end tests in `internal/cli`, `internal/daemon` and `internal/server`
  start a real server (`internal/testutil`) and drive real clients against it.
  The CLI end-to-end tests run the compiled command, so the suite takes about
  a minute.
- `internal/connector/fake` is a scripted connector for testing the store,
  outbox, notifier and socket API without a server.
- TUI tests send key and mouse messages to the model and compare rendered
  frames; golden frames are in `internal/tui/testdata`.
- What cannot be automated is in [the manual checklist](guides/manual-checklist.md).

## Releases

`.goreleaser.yaml` builds darwin and linux for amd64 and arm64 plus windows
amd64, archives them with `LICENSE` and `README.md`, writes `checksums.txt`
and updates the Homebrew formula in `jpe0824/homebrew-tap`. The release
workflow runs on tags matching `v*`.

To release: merge to `main`, then push a tag (`git tag v0.1.0 && git push origin
v0.1.0`). The workflow runs the tests, builds, publishes the GitHub release,
and pushes `Formula/termchat.rb` to the tap over SSH with a deploy key held in
the `HOMEBREW_TAP_DEPLOY_KEY` secret. With that secret empty (a fork, a
snapshot) the formula is generated in `dist/` and not pushed.

The version string is `cli.Version`: set by
`-ldflags "-X github.com/jpe0824/termchat/internal/cli.Version=..."` in
releases and `make build`; otherwise the module version from the build (so
`go install ...@v0.1.0` reports `v0.1.0`); otherwise `dev`.
`make release-snapshot` builds the archives locally without publishing.
