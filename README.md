# termchat

A fast, reliable, chat-only TUI that runs in any terminal.

termchat is one binary that is a self-hosted chat server, a per-user background
process, a command-line client for people and AI agents, and a terminal
interface. A team runs one server on a private network; everyone chats from
their terminal.

> **Security: private networks only.** This version is built for a private
> network or VPN. Do not expose the server to the internet. There is no
> end-to-end encryption: traffic is encrypted in transit, but whoever runs the
> server can read every message. Turn on disk encryption on the server machine.
> See [the server guide](docs/guides/server.md).

```
┌ termchat ────────────────────────────────────────────────────────────────────────── team.example ● connected ┐
│ Direct             │ # platform                                                         │ Members (4)        │
│ ● dana           2 │ ────────────────────────────────────────────────────────────────── │ ci-bot [agent]     │
│   sam              │ 10:02  dana    did the meter import finish?                        │ dana               │
│   ci-bot           │ 10:03  you     yes, 10 min ago                                     │ jason (you)        │
│ Rooms              │ 10:41  dana    great. one row looks off                            │ sam                │
│ ● # platform     5 │ 10:42  ci-bot  [agent] build 812 passed                            │                    │
│   # deploys        │ 10:44  sam     @jason can you look? (edited)                       │ Open room          │
│   # random         │ 10:45  you     on it                                               │ created by dana    │
│                    │ 10:45  you     pushing a fix now  sending…                         │                    │
│                    │ ────────────────────────────────────────────────────────────────── │                    │
│                    │ >                                                                  │                    │
│                    │ ────────────────────────────────────────────────────────────────── │                    │
│                    │ ⏎ send  alt+⏎ newline  ↑ select  esc leave                         │                    │
│                    │                                                                    │                    │
└─ ctrl+k jump  ctrl+n new  ctrl+o room  ctrl+c quit ──────────────────────────────────────────────────────────┘
```

## Goals

- **Chat only.** Text conversations between people and agents on one server. No feature sprawl.
- **Fast and reliable.** Instant startup, live delivery. A message you typed never vanishes without a visible failure and is never delivered twice.
- **Keyboard and mouse navigation.** Fully usable with either.
- **Open source, local storage.** MIT licensed. History is stored on your machine and on your team's server, nowhere else.
- **Works in any terminal.** No dependence on a specific emulator or font. Notifications work in any terminal.
- **Agent CLI.** Agents read and send messages through the command line, without driving the TUI. This is also how termchat hooks into AI CLIs such as Herdr.
- **Later:** attachments as opt-in downloads, and connectors for Teams, Slack and Discord. Neither exists yet.

## Non-goals

- **Not a replacement for other chat clients' full UIs.** Future connectors cover chat, not every feature of a platform.
- **Not an AI chat interface.** termchat runs no model. AI tools reach it through the CLI.
- **No calling or screen sharing.**
- **No federation or clustering.** One server is one team on one machine.

## How it works

- `termchat serve` runs the server: one process, one SQLite file, one port.
  Clients connect over the SSH protocol with a per-device key, so there are no
  certificates, passwords or domain names to set up.
- On each person's machine a background process holds the connection, the local
  history, the outbox and notifications. It starts the first time you run any
  client command and keeps running after you close the interface, so
  notifications arrive with the interface closed. Stop it with `termchat daemon stop`.
- The interface and the command-line client both talk to that background
  process, so they always show the same state.

## Install

| Route | Command |
|---|---|
| Homebrew (macOS, Linux) | `brew install jpe0824/tap/termchat` |
| Go 1.27.4 or later | `go install github.com/jpe0824/termchat/cmd/termchat@latest` |
| Release archive | Download `termchat_<version>_<os>_<arch>` from the GitHub releases page, unpack, and put `termchat` on your `PATH`. |
| From source | `git clone https://github.com/jpe0824/termchat && cd termchat && make build`, then use `./termchat`, or `make install` to copy it to `~/go/bin`. |

The Homebrew and release-archive routes are available from the first release.
Until then, use `go install` or build from source.

Targets: macOS and Linux (amd64, arm64) for client and server. The Windows
build runs the server; the background process the client needs is not
supported on Windows.

## Quick start

Two people, Ada and Bob. Ada runs the server. This block listens on
`127.0.0.1`, which is right for trying it on one machine. For a team, use the
server's address on your private network instead (for example
`--addr 10.0.0.5:7420`), and give people that address. Without `--addr`, the
server listens on every network interface, so only leave it off on a machine
that is itself on a private network.

On the server machine, create the first admin and start the server:

```sh
termchat server init --data ~/termchat-server --handle ada --name "Ada"
termchat serve --data ~/termchat-server --addr 127.0.0.1:7420
```

`init` prints an invite code (default port 7420). Leave `serve` running and, in
another terminal on Ada's computer, join with the code and the server's address,
and check the connection:

```sh
termchat join 127.0.0.1:7420 --invite <code>
termchat status      # repeat until the state is "connected"
termchat
```

`join` returns while the connection is still starting, so wait for `connected`
before the next command. `termchat` with no arguments opens the interface. Ada
invites Bob from her computer (admins only), or with
`termchat server invite bob --data ...` on the server machine:

```sh
termchat invite bob --name "Bob"
```

Invite codes are valid for 24 hours and work once. On Bob's computer, run the
`termchat join ...` line it printed (with Bob's own address for the server),
then `termchat status` until it shows `connected`. Bob needs his own config,
data and runtime directories if he is on the same machine as Ada; see
[the agent guide](docs/guides/agent-cli.md#acting-as-yourself-or-as-an-agent).
Then either of them can chat from the interface, or from the command line:

```sh
termchat dm ada               # on Bob's computer: open the direct chat
termchat send @ada "hello"    # send
termchat read @ada            # read
termchat wait @ada            # block until Ada's next message arrives
```

## For AI agents

Every client command works without the interface, prints JSON when its output
is not a terminal (or with `--json`), and uses distinct exit codes.

```sh
termchat chats --json
termchat send '#platform' "build 812 passed"
termchat wait '#platform' --mentions --timeout 5m
```

`wait` blocks until someone else posts and prints that one message. `read`,
`wait` and `search` never mark anything read, so an agent working as you does
not hide messages from you. `termchat agent create <handle>` makes a separate
agent account with its own key. See [the agent CLI guide](docs/guides/agent-cli.md).

## Guides

- [Running a server](docs/guides/server.md): install, backup, systemd, Docker, upgrading.
- [Getting started](docs/guides/getting-started.md): joining, the interface, config file.
- [Agent CLI](docs/guides/agent-cli.md): commands, JSON, exit codes, agent loops.
- [Notifications](docs/guides/notifications.md): routes, rules, hooks.
- [Terminals and paste](docs/guides/terminals-and-paste.md)
- [Architecture](docs/termchat.md), for contributors.
- [Manual release checklist](docs/guides/manual-checklist.md)
- [Project memory](docs/memory/context.md): decisions, limits and deferred work.

## Development

```sh
make lint test build     # vet, test, build ./termchat
make release-snapshot    # build every release archive into dist/ (needs goreleaser)
```

## License

[MIT](LICENSE)
