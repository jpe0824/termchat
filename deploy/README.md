# Deploying a termchat server

Read the [server guide](../docs/guides/server.md) first. In this version the
server belongs on a private network or behind a VPN, not on the internet, and
whoever runs it can read the messages.

Two ways to run it are provided here: systemd and Docker.

## systemd

`termchat.service` runs `termchat serve --data /var/lib/termchat` as the
`termchat` user, restarts it on failure, and limits what it can touch
(`NoNewPrivileges`, `ProtectSystem=strict`, `ReadWritePaths=/var/lib/termchat`,
`PrivateTmp` and more).

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin termchat
sudo install -d -o termchat -g termchat -m 700 /var/lib/termchat
sudo install -m 755 termchat /usr/local/bin/termchat
sudo -u termchat termchat server init --data /var/lib/termchat --handle ada --name "Ada"
sudo cp deploy/termchat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now termchat
journalctl -u termchat -f
```

`server init` prints the first invite code. To listen on one address only, or to
set retention, add `--addr 10.0.0.5:7420` or `--retention 2160h` to `ExecStart`.
Run later `termchat server ...` commands as `sudo -u termchat`.

## Docker

The image holds one static binary, runs as a non-root user, keeps all state in
the `/data` volume, exposes port 7420, and defaults to
`termchat serve --data /data`. `TERMCHAT_SERVER_DATA=/data` is set, so
`termchat server ...` commands in the container need no `--data`.

Build from the repository root:

```sh
docker build -f deploy/Dockerfile --build-arg VERSION=$(git describe --tags --always) -t termchat .
```

First run: create the volume and the first admin in a throwaway container, then
start the server. The `init` command prints the first invite code.

```sh
docker volume create termchat-data
docker run --rm -v termchat-data:/data termchat server init --handle ada --name "Ada"
docker run -d --name termchat --restart unless-stopped \
  -p 10.0.0.5:7420:7420 -v termchat-data:/data termchat
```

Replace `10.0.0.5` with the private address of the host; `-p 7420:7420` would
publish the port on every interface. Docker publishes ports by editing the
firewall, so check that the port is not reachable from outside your network.

Administration while the server runs:

```sh
docker exec termchat termchat server invite bob --name "Bob"
docker exec termchat termchat server user list
docker logs -f termchat
```

Backup and restore, with the passphrase in the environment:

```sh
docker exec -e TERMCHAT_BACKUP_PASSPHRASE='a long passphrase' termchat \
  termchat server backup /data/backup.tcbackup
docker cp termchat:/data/backup.tcbackup .
```

To restore, run it into an empty volume before starting the server:

```sh
docker run --rm -e TERMCHAT_BACKUP_PASSPHRASE='a long passphrase' \
  -v termchat-data:/data -v "$PWD/backup.tcbackup:/backup.tcbackup:ro" \
  termchat server restore /backup.tcbackup
```

A bind mount instead of a named volume must be
writable by user ID 65532 (`chown 65532:65532 <dir>`).

Upgrade by building the new image, then `docker stop termchat && docker rm
termchat` and running it again with the same volume.
