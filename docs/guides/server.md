# Running a termchat server

> **Read this first.**
>
> - **Run the server on a private network or behind a VPN. Do not expose it to
>   the internet in this version.** It has per-connection rate limits and a cap
>   on failed sign-in attempts, but it has not been hardened or tested for a
>   public address.
> - **There is no end-to-end encryption.** Traffic between clients and the
>   server is encrypted in transit (the SSH protocol), but the server stores
>   message text readably. Whoever runs the server, or can read its disk or its
>   backups' passphrase, can read every message.
> - **Turn on disk encryption** on the machine that holds the data directory
>   (FileVault, LUKS, BitLocker). termchat does not encrypt its own database.

A termchat server is one process, one SQLite file and one TCP port. It needs
no database service, certificates or domain name. It is sized for one team:
tens to low hundreds of people on one small machine. There is no clustering
and no federation between servers.

## Install

Install `termchat` on the server machine by any route in the
[README](../../README.md#install). Check it with `termchat --version`.

## First run

Pick a data directory. It holds the database (`server.db`) and the server's
host key (`host_key`); keep it private and backed up. termchat creates both,
and the database's `-wal` and `-shm` files, with mode 0600 even in a directory
you made yourself. Create the first admin:

```sh
termchat server init --data /var/lib/termchat --handle ada --name "Ada"
```

```
created admin ada
invite code: WV63SGAIRA (valid for 24h0m0s)
on the client, run (replace <host:port> with the address clients reach this server at; the default port is 7420):
  termchat join <host:port> --invite WV63SGAIRA
```

`init` works only on a server with no people. It creates the data directory if
it does not exist. Handles are lowercase letters, digits, `-` and `_`, start
with a letter or digit, and are at most 32 characters.

Every `termchat server ...` command and `termchat serve` need `--data <dir>`
(or the environment variable `TERMCHAT_SERVER_DATA`). There is no default, so a
server never starts on a directory by accident.

## Serve

```sh
termchat serve --data /var/lib/termchat
```

```
listening on [::]:7420
host key SHA256:IAOOESd9JW6mf6VOm+wG3sJmtmEUD4PFDeKU9X4nd7A
```

| Flag | Default | Meaning |
|---|---|---|
| `--data <dir>` | none; required | Data directory (or `TERMCHAT_SERVER_DATA`). |
| `--addr <address>` | `:7420` | Address to listen on. `:7420` listens on every interface; give a private address such as `10.0.0.5:7420` to listen on one. |
| `--retention <duration>` | `0` | Erase the text of messages older than this, for example `2160h` (90 days). `0` keeps everything. Checked at start and then hourly. |

The server stops cleanly on `SIGINT` or `SIGTERM`. Administration commands such
as `server invite` can run while it serves.

Tell people to join with the address your machine has on the private network,
for example `termchat join chat.internal:7420 --invite <code>`. A client that
is given no port uses 7420.

## Invites

An invite is a one-time code, valid for 24 hours.

```sh
termchat server invite bob --name "Bob"
```

```
invite creates the new person bob
invite code: 4N5FHFRB65 (valid for 24h0m0s)
...
  termchat join <host:port> --invite 4N5FHFRB65
```

- For a new handle it creates the person when the code is redeemed. `--name`
  sets the display name.
- For an existing handle it adds another device for that person.
- A handle that was removed cannot be invited again.

Admins can also create invites from their own computer with
`termchat invite <handle>`. People add their own devices with
`termchat device add`.

## People, keys, rooms, admins

```sh
termchat server user list --data D           # handle, display name, admin/agent flags
termchat server user remove bob --data D     # leaves rooms, revokes all keys
termchat server user admin bob --data D      # grant admin
termchat server user admin bob --revoke --data D
termchat server key list bob --data D        # fingerprints and when added
termchat server key remove SHA256:... --data D
termchat server room list --data D           # name, open or private, member count
```

- Removing a key disconnects that device at once. Removing a person revokes
  every key they have and takes them out of their rooms. Members who are
  connected while `user remove` runs see the removal when the next event in
  that room reaches them, or when they reconnect: the command writes to the
  database from its own process, so the running server does not push it.
- An invite is either for a new person or for another device of an existing
  one, fixed when it is made: `server invite` makes the first for a free handle
  and the second for an existing one. A new-person invite is refused once its
  handle exists, and a device invite once its handle is gone. Invites made by a
  version of termchat before this rule are refused; make new ones.
- The last admin cannot be removed or demoted. Make someone else an admin first.
- Agent accounts (`termchat agent create`) cannot be admins.
- `room list` shows rooms only, not direct messages.
- Admin commands other than `init` need an existing database in `--data`, which
  catches a mistyped directory.
- Admins can create invites from their own computer (`termchat invite`).
  First-time setup, backup, restore and removing people or keys happen on the
  server machine.

## Backup and restore

```sh
export TERMCHAT_BACKUP_PASSPHRASE='a long passphrase'
termchat server backup /backups/termchat-2026-11-14.tcbackup --data /var/lib/termchat
```

- The backup is one file containing the database (a consistent snapshot, safe
  to take while the server runs) and the host key, encrypted with the
  passphrase. It is written with mode 0600 and never overwrites an existing
  file.
- `TERMCHAT_BACKUP_PASSPHRASE` supplies the passphrase for `backup` and
  `restore`. If it is unset and stdin is a terminal, termchat prompts (twice
  for `backup`). If it is unset and there is no terminal, the command fails.
- Keep the passphrase somewhere other than the backup. A lost passphrase makes
  the backup unreadable.

Restore into an empty or new directory:

```sh
termchat server restore /backups/termchat-2026-11-14.tcbackup --data /var/lib/termchat
```

Restore refuses a directory that is not empty and, on a wrong passphrase,
creates nothing. Because the host key is restored, clients keep trusting the
server.

## systemd

`deploy/termchat.service` in the repository runs the server as a dedicated user
with `NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp` and write access
only to `/var/lib/termchat`.

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin termchat
sudo install -d -o termchat -g termchat -m 700 /var/lib/termchat
sudo install -m 755 termchat /usr/local/bin/termchat
sudo -u termchat termchat server init --data /var/lib/termchat --handle ada --name "Ada"
sudo cp deploy/termchat.service /etc/systemd/system/termchat.service
sudo systemctl daemon-reload
sudo systemctl enable --now termchat
```

Edit `ExecStart` to add `--addr` or `--retention`. Logs go to the journal:
`journalctl -u termchat`. Run later `termchat server ...` commands as the
`termchat` user (`sudo -u termchat ...`) so the files keep their owner.

## Docker

`deploy/Dockerfile` builds a static binary into a minimal image that runs as a
non-root user, keeps data in the `/data` volume and exposes port 7420. See
`deploy/README.md` for the commands, including first-run `server init`.

## Upgrading

1. Take a backup.
2. Install the new binary and restart the server (`systemctl restart termchat`,
   or recreate the container). Database migrations run at start.
3. Each person should run `termchat daemon stop` after upgrading their own
   client. Their background process keeps running the old version until it is
   stopped; the next client command starts one from the new binary. The
   interface shows "background process is a different version" with the
   command to run when the two cannot talk to each other.

A client and a server that speak different protocol versions refuse to talk and
the client shows "server version not supported". Upgrade the older side.

## What a changed host key means

A client records the server's host key the first time it joins and refuses to
connect if the key later differs. Its status becomes `host_key_changed` and the
interface shows "server key changed".

- If you restored the server from a backup, or never lost `host_key`, the key
  is unchanged and clients keep working.
- If you rebuilt the server without the old `host_key` (a new data directory),
  every client sees a changed key. Confirm the change is yours, then on each
  client delete the line for that server from the `known_servers` file in the
  client's data directory and run `termchat join <host:port> --invite <code>`
  again; `join` records the new key and replaces the stuck account. A rebuilt
  server has a new database, so each person needs a new invite. Compare the
  fingerprint `join` prints with the server's before using the client.
- If you did not change the server, treat it as a warning that something is
  answering in the server's place, and do not remove the line.

Compare fingerprints: `termchat serve` prints `host key SHA256:...` at start.
On a client, `termchat join` prints `server key SHA256:...` for the key it
recorded, and `termchat status` shows the same value as `server key`
(`server_key` in `--json`). They must match; check them on first join, before
the invite code is trusted to that server.

## A device that was revoked

A device whose key was removed shows `not_authorised` and stops connecting.
To bring it back, give that person a new invite (`termchat server invite
<handle>` on the server machine, or `termchat device add` from another of their
devices) and run `termchat join <host:port> --invite <code>` on the revoked
device. `join` may replace a configured account only when it names the same
server and the connection is `not_authorised` or `host_key_changed`; otherwise
it refuses with "an account is already configured".
