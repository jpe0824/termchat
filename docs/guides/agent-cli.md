# The termchat command line, for scripts and AI agents

Every client command works without the interface. They are clients of the
background process (see [getting started](getting-started.md#the-background-process)),
so they see the same messages as the interface. The first command you run
starts the background process if it is not running.

## Output: `--json` and the pipe rule

Every client command takes `--json` and `--account <name>`.

- With `--json`, or whenever **stdout is not a terminal** (a pipe, a file,
  `$(...)`), a command prints JSON: one line, one value.
- On a terminal without `--json` it prints a readable table.
- Errors go to stderr as one line, `termchat: <message>`, and stdout is empty.
  The exit code says what kind of error (below).
- `--account` names the account. This version has one account, so it can be
  left out; a name that is not the configured account is a usage error (exit 2).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Any other failure: the background process stopping during the command, or a request the server refuses as invalid, for example `termchat room join <room>` for a room you are already in (`termchat: invalid request: already a member`). |
| 2 | Usage error: bad flag or argument, invalid handle or room name, empty or oversized message text. |
| 3 | Not found: no such chat, person or room, or an ambiguous chat name. |
| 4 | No account joined, not connected to the server, not authorised (key removed), or forbidden. |
| 5 | Timeout (`wait --timeout`). |
| 6 | Sending from the command line is disabled (`cli.send = "deny"`). |
| 130 | Interrupted with Ctrl-C. |

Sending while the server is unreachable is not an error: the message is queued
and `send` exits 0 (see below).

## Naming a chat

`<chat>` is `@handle` for a direct message, `#room` for a room, or a
conversation id (the `id` field in JSON). Case is ignored. A bare name such as
`platform` is tried as a room and as a person; if both exist it is ambiguous
(exit 3), so use the sigil in scripts. `@handle` needs a direct conversation
to exist: run `termchat dm <handle>` first (it creates the conversation and
prints its id). `#room` also finds open rooms you have not joined.

## Commands

### Account and process

```sh
termchat join <host:port> --invite <code>   # join a server with an invite code
termchat status                             # account and connection state
termchat daemon start                       # make sure the background process runs
termchat daemon stop                        # stop it; prints nothing
```

`join`, `status` and `daemon start` print this JSON (table: `account`, `handle`,
`state`, `pending`, `syncing`, `server key`):

```json
{"account":"ada","handle":"ada","state":"connected","pending":0,"syncing":false,"server_key":"SHA256:..."}
```

`server_key` is the fingerprint of the server key pinned at `join`.
`set_aside` appears when this join, or the background process since it
started, moved the earlier local history aside because it was filled under
another server key; it is the directory it went to. `problem` appears when the
account could not be brought up (for example the history could not be moved);
`state` is then `offline` and other commands fail with the reason (exit 1). `join --server-key
SHA256:...` refuses a server with another key before the invite code is sent
(exit 1); a value not exactly in that form (43 base64 characters, no padding or
whitespace) is a usage error (exit 2).

`state` is `connecting`, `connected`, `offline`, `not_authorised`,
`host_key_changed` or `version_mismatch`. `pending` counts messages waiting to
be sent. `status` exits 4 when no account is joined.

### Reading

```sh
termchat chats [--all]                      # conversations, most recent first
termchat read <chat> [-n 20] [--before <seq>]   # oldest to newest
termchat search <query...> [-n 20]          # every word, newest first
termchat mark-read <chat>                   # move your read position
termchat dm <handle>                        # open or create a direct conversation
```

`chats` prints an array. `--all` adds open rooms you have not joined, marked
`"joined": false`:

```json
[{"id":"r_1d6c074850726d7d","kind":"room","name":"team","unread":1,"last_at":"2026-11-14T23:26:57Z"},
 {"id":"dm:ada:bob","kind":"dm","name":"bob","unread":1,"last_at":"2026-11-14T23:26:52Z"}]
```

`kind` is `room` or `dm`; for a DM, `name` is the other person's handle;
`last_at` is `null` when the conversation has no messages.

`read` and `search` print an array of messages; `wait` prints one message
object. A message:

```json
{"conv":"r_1d6c074850726d7d","seq":4,"msg_id":"34b95455-f776-48fe-8edf-8fab91816e9b",
 "author":"bob","agent":false,"at":"2026-11-14T23:26:54Z","text":"@ada ping",
 "mentions":["ada"],"edited":false,"deleted":false}
```

- `seq` is the server's number for the event in that conversation. It increases
  within a conversation and is what `--before` and `--since` use. Your own
  messages that are not delivered yet have `seq` 0.
- `status` is present only on your own undelivered messages: `"sending"` or
  `"failed"`, and a failed one carries `"error"` with the reason.
- `agent` is true for messages from an agent account. `deleted` is true for a
  deleted message (its text is gone). `mentions` lists the handles mentioned.
- `-n` is how many messages (default 20, minimum 1). `--before <seq>` returns
  only messages below that number, to page back.
- `search` tokenises the query: it matches messages containing every word, in
  any conversation, from the local history, newest first.

**`read`, `wait` and `search` never mark anything read.** Only the interface
and `mark-read` move your read position, so an agent acting as you never hides a
message from you. `mark-read` prints nothing.

### Sending

```sh
termchat send [flags] <chat> [text]
```

```json
{"msg_id":"0565a6e2-8e36-489d-92ad-270794432b97","state":"sending"}
```

- `send` returns at once with the message id and state `sending`; it does not
  wait for delivery. The message is in the outbox and goes out as soon as the
  server is reachable, exactly once. See it with `read` (the `status` field) or
  `status` (`pending`).
- **Flags go before the chat.** Everything after `<chat>` is the text, even if it
  starts with a dash: `termchat send --json @bob "- done"` sends `- done`.
  `--` before the chat also works.
- With no text, or `-`, the text is read from stdin. One trailing newline is
  dropped; line breaks inside the text are kept and make one multi-line message.
  `echo "build passed" | termchat send '#ci'`
- Empty text, or text over 8192 bytes, is a usage error (exit 2).
- `cli.send = "deny"` in `config.toml` makes `send` fail with exit 6 and the
  message `sending from the command line is disabled (cli.send = "deny")`.
  Use it on a machine where scripts should read but never write as you.
  Restart the background process after changing it.

### Waiting

```sh
termchat wait [<chat>] [--timeout <duration>] [--since <seq>] [--mentions]
```

`wait` prints the next message from someone else, in one chat or in any, and
exits 0. It never returns your own messages, deleted messages or edits.

- `--timeout 30s` (or `5m`, `1h`): exit 5 with an empty stdout if nothing
  arrives in time. Without it, `wait` waits forever.
- `--mentions`: only messages that mention you.
- `--since <seq>`: needs a chat. If a matching message with a higher `seq` is
  already stored, the earliest one is printed at once. Use it so nothing is
  missed between two calls: remember the `seq` of the last message you handled
  and pass it next time. Without `--since`, `wait` sees only messages that
  arrive after it starts.
- If the background process stops while `wait` is waiting, `wait` exits 1 with
  `termchat: connection to the background process closed`. Run it again; the
  next command starts a new process. With `--since` set to the last `seq` you
  handled, nothing is lost.

### Rooms, invites and agents

```sh
termchat room create <name> [--private]
termchat room join|leave <room>
termchat room invite|remove <room> <handle>
termchat room rename <room> <name>
termchat invite <handle> [--name <display name>]     # admins only
termchat device add                                  # code for another device of yours
termchat agent create <handle> [--name <display name>]
```

The room commands print the resulting conversation, `{"id","kind","name"}`.
`invite`, `device add` and `agent create` print the code and the command to run:

```json
{"code":"BS4P5BNXNO","join":"termchat join localhost:7420 --invite BS4P5BNXNO"}
```

Rooms are open to everyone on the server unless created with `--private`. A
room's creator and admins manage its members. An unknown person is exit 3.

## Acting as yourself or as an agent

**As yourself.** A script on your machine uses your account. Messages go out
under your handle; the outbox records that they came from the command line, and
`cli.send = "deny"` turns that off. Use this for personal automation.

**As an agent.** An agent account has its own handle and key, and its messages
are labelled `[agent]` (`"agent": true`) wherever they are shown. Create one:

```sh
termchat agent create ci-bot --name "CI bot"
```

On the machine the agent runs on, run the printed `termchat join` line. A
background process serves one account, so when the agent runs on the same
machine as you, give it its own directories:

```sh
export TERMCHAT_CONFIG_DIR=$HOME/.ci-bot/config
export TERMCHAT_DATA_DIR=$HOME/.ci-bot/data
export TERMCHAT_RUNTIME_DIR=/tmp/ci-bot        # keep it short
termchat join chat.internal:7420 --invite <code>
```

You own the agents you create. Agents cannot be admins. termchat runs no
model; an agent is an outside program given its own identity.

## Worked example: an agent loop

Reply to every mention in `#platform`, never missing a message and surviving a
restart of the background process. It needs `jq`; `my-agent` is whatever
produces the reply.

```sh
#!/bin/sh
chat='#platform'
since=$(termchat read "$chat" -n 1 | jq '.[0].seq // 0')

while true; do
  msg=$(termchat wait "$chat" --since "$since" --mentions --timeout 10m)
  case $? in
    0) ;;
    5) continue ;;                # nothing in 10 minutes; wait again
    *) sleep 5; continue ;;       # not connected, or the process stopped
  esac
  since=$(printf '%s' "$msg" | jq '.seq')
  text=$(printf '%s' "$msg" | jq -r '.text')
  printf '%s' "$text" | my-agent | termchat send "$chat"
done
```

`since` moves only when a message was handled, so a failed round repeats the
same wait. Messages sent while the agent was down are picked up by `--since`.
