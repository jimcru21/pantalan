# Installation

Everything about putting Pantalan on a server and keeping it there: what
the installer does, its flags, the firewall, upgrades, rollbacks,
uninstalling and verifying a release. For the short version, see the
[Quick start](../README.md#-quick-start).

## Install

On a fresh Debian 12, Debian 13, Ubuntu 24.04 LTS or Ubuntu 26.04 LTS
server (amd64 or arm64), as a user who can `sudo`, download the installer,
then run it:

```sh
curl -fL --max-time 120 -o install.sh https://github.com/jimcru21/pantalan/releases/latest/download/install.sh
sudo sh install.sh
```

Downloading first is the safer way to run it:

- **A failed download doesn't hang silently.** You see the progress and any
  error. If GitHub's download servers accept the connection and then send
  nothing, `--max-time` stops the download instead of waiting forever.
- **You can read or check it before it runs as root.** To check it against
  the release's checksums:

  ```sh
  curl -fL --max-time 120 -O https://github.com/jimcru21/pantalan/releases/latest/download/SHA256SUMS
  sha256sum -c --ignore-missing SHA256SUMS
  ```

The installer's own downloads (the binary and `SHA256SUMS`) give up after
four tries on a server that stops sending, rather than hanging.

Flags go after `install.sh`, for example for cloud-init or CI, where there's
no terminal to ask on:

```sh
sudo sh install.sh --admin-user alice --dashboard-port 8443
```

The one-line form `curl -fsSL <url> | sudo sh -s -- [flags]` also works.

| Flag | |
|---|---|
| `--admin-user NAME` | The Admin's username. Asked for on the terminal when missing; with no terminal, the install stops and names the flag. When an Admin already exists it is kept, and a differing `--admin-user` is ignored with a warning saying so. |
| `--dashboard-port N` | The Dashboard's HTTPS port, default `8443`. Ports below 1024 (80 and 443 among them), the ports SSH listens on and the app proxy's port (`PANTALAN_PROXY_ADDR` in the env file, `80` when unset) are refused. When the default is taken and the flag wasn't given, the installer names what holds it and asks on the terminal, suggesting the next free port; with no terminal, it stops and names the flag. It never picks a port by itself. |
| `--version vX.Y.Z` | Install that release instead of the one the script came with. |
| `--no-firewall` | Leave the firewall entirely alone, on this run and every re-run (stored as `PANTALAN_FIREWALL=off`). |

Once installed, the env file holds the settings: a re-run reads the
Dashboard port and firewall mode from it, and a flag that disagrees with it
stops the run before changing anything. To change one, edit
`/etc/pantalan/pantalan.env`, then `sudo systemctl restart pantalan`. For
the Dashboard port, run the installer again (without the flag) instead of
restarting, so its firewall rule moves too.

Running the command is the consent: there's no "continue?" prompt. The
installer first checks this Node without changing anything (root, the
distro and architecture, systemd, Docker, and that the app proxy's port
(80, unless `PANTALAN_PROXY_ADDR` in the env file moves it), 443 and the
Dashboard port are free), then downloads the release binary and verifies it
against the release's `SHA256SUMS`, and checks the Admin username with it.
Only then does it change the Node:

- installs `git` and `ca-certificates`, and Docker CE from Docker's apt
  repository when no Docker is installed (with `"live-restore": true`, so
  app containers survive Docker upgrades). An existing Docker CE 28 or newer
  is used as it is and never upgraded; any other Docker (`docker.io`,
  `podman-docker`, snap, Docker CE older than 28) stops the install, and
  nothing is removed.
- creates the `pantalan` system user (no login shell, in the `docker`
  group) and these files:

  | Path | Owner, mode | |
  |---|---|---|
  | `/usr/local/bin/pantalan` | root, `0755` | the binary, replaced atomically |
  | `/etc/pantalan/pantalan.env` | root:pantalan, `0640` | settings, including a generated Admin Token |
  | `/var/lib/pantalan/` | pantalan, `0750` | the database, `tls/`, `builds/` |
  | `/etc/systemd/system/pantalan.service` | root, `0644` | owned by the installer and rewritten on every run: put your changes in `pantalan.service.d/` |
  | `/var/log/pantalan-install.log` | root, `0600` | the install's full output, minus the Admin password |

- creates the Admin with a generated password, starts the service and waits
  for it to answer on HTTPS (printing the service's last 50 log lines if it
  doesn't).
- last, configures ufw so only SSH, 80, 443 and the Dashboard port are
  reachable (see [Firewall](#firewall)).

It ends with a summary: the version, the Dashboard URL, the certificate
fingerprint to compare with your browser's warning, the Admin and the
one-time Admin password, where the Admin Token is, how to recover a lost
password, and what happened to the firewall. The Admin Token is never
printed. The password is shown only on the terminal and kept out of the
install log, but an install with no terminal (cloud-init, CI) leaves it in
that tool's own log: run `sudo pantalan admin reset-password` afterwards if
that matters.

If a step fails, the installer names it and the log; fix the cause and run
the same command again.

**The `docker` group is effectively root.** The service runs as `pantalan`
in a hardened unit (`ProtectSystem=strict`, `NoNewPrivileges`, only
`CAP_NET_BIND_SERVICE`), but anything that can talk to Docker can get root
on the Node, so that hardening is defence in depth, not a boundary.

### Firewall

The installer allows SSH, 80, 443 and the Dashboard port (TCP, IPv4 and
IPv6) in ufw, installing ufw if needed. Its rules carry the comment
`pantalan` (80, 443, the Dashboard port: made to match those on every run,
so moving the Dashboard moves its rule) or `pantalan-ssh` (one per SSH
port, only ever added, `allow` rather than `limit` so automation isn't
throttled). SSH ports are everything sshd and `ssh.socket` listen on,
every `Port` in `sshd -T` and the port your session came in on, all
allowed before ufw is enabled, so enabling it doesn't lock you out. It
never runs `ufw disable` or `ufw reset`, never edits `/etc/default/ufw` and
adds no `DOCKER-USER` rules. What it does depends on the Node:

| Node | What happens |
|---|---|
| ufw inactive | Rules added, ufw enabled. A re-run enables it again if you disabled it by hand. |
| ufw already active | Rules added only. Your rules and default policies are left as they are (a rule of yours for the same port stands in for Pantalan's); a default incoming policy other than deny or reject gets a warning. |
| firewalld active, or `nftables.service` enabled with your own ruleset | ufw skipped: not installed, no rules. Open 80, 443 and the Dashboard port in that firewall. (The stock `/etc/nftables.conf` that Docker CE's dependencies bring doesn't count.) |
| `MANAGE_BUILTINS=yes` or `IPV6=no` (with a global IPv6 address) in `/etc/default/ufw`, Docker's nftables firewall backend, or no SSH server found | Rules staged: added, but ufw left off. Check SSH, then run `sudo ufw --force enable`. |
| `--no-firewall` (`PANTALAN_FIREWALL=off`) | Nothing at all. |

Staged and skipped firewalls still end the install with success, since
Pantalan is running. `PANTALAN_FIREWALL` is read only by the installer; set
it to `off` in the env file to stop it managing the firewall.

Two things are outside ufw's reach:

- **Ports Docker publishes bypass ufw.** Docker forwards them itself, ahead
  of ufw, so a container published on `0.0.0.0` (Docker's default) is
  reachable from anywhere whatever ufw says. Pantalan's own app containers
  publish on `127.0.0.1` only; the summary warns about any running
  container that doesn't, whatever the firewall mode (Docker forwards those
  ports ahead of firewalld or an nftables ruleset too, and with
  `--no-firewall` nothing filters them).
- **Cloud security groups are separate.** A provider's firewall in front of
  the server knows nothing of ufw: open 80, 443 and the Dashboard port
  there too.

### Upgrade and repair

Run the install command again. There's no separate upgrade mode: every step
checks what's already there and does only what's missing, so the same
command installs, upgrades, repairs a damaged install (a deleted unit, say)
and resumes one that failed part-way. It needs no flags and no terminal,
since the Admin, the Admin Token and the settings already exist; the Admin
prompt appears only when there's no Admin.

The installer compares the release it installs (the latest, or
`--version vX.Y.Z`) with what `pantalan version` says is installed:

- **Newer:** the binary is replaced atomically and the service restarted.
  The Admin, sessions, apps and the certificate carry over. If the new
  release changes the database schema, the service snapshots the database
  to `/var/lib/pantalan/pantalan.db.bak-<schema version>` before migrating
  it (the two most recent snapshots are kept). The summary opens with
  `Upgraded vA → vB` and has no Admin or password lines.
- **The same:** a repair. Anything missing is put back, and the service is
  restarted.
- **Older:** refused, before anything changes. See
  [Rolling back](#rolling-back).

The unit file is rewritten on every run; your own changes in
`/etc/systemd/system/pantalan.service.d/` are left alone.

### Rolling back

The installer doesn't downgrade, because a newer release may have migrated
the database to a schema the older one doesn't know (an older binary
refuses such a database rather than damage it). To go back, remove the
newer binary, restore the snapshot taken when the database was migrated,
and install the older release (with `install.sh` downloaded as in
[Install](#install)):

```sh
sudo sh install.sh --uninstall
sudo ls -l /var/lib/pantalan/   # pick the pantalan.db.bak-<version> taken at the upgrade you're undoing
sudo install -m 0600 -o pantalan -g pantalan /var/lib/pantalan/pantalan.db.bak-<version> /var/lib/pantalan/pantalan.db
sudo rm -f /var/lib/pantalan/pantalan.db-wal /var/lib/pantalan/pantalan.db-shm
sudo sh install.sh --version v1.2.3
```

`--uninstall` stops the service and removes the binary but keeps the data,
config and Admin (see [Uninstall and purge](#uninstall-and-purge)), and with
no binary installed there's nothing to compare against, so the older
release installs. Removing the `-wal` and `-shm` files matters: they belong
to the newer database. Anything that changed after the snapshot was taken
(apps, deployments, a new password) is lost. With no snapshot, the database
was never migrated in place: skip the restore. If the older release still
can't read the database, the install stops at "Checking the Admin username"
with the database's error and changes nothing else.

### Uninstall and purge

With `install.sh` downloaded as in [Install](#install):

```sh
sudo sh install.sh --uninstall
```

`--uninstall` stops and disables the service and removes its unit and
`/usr/local/bin/pantalan`. It keeps everything else and lists what it kept:
`/etc/pantalan/` (settings and the Admin Token), `/var/lib/pantalan/` (the
database, TLS certificate and builds), your `pantalan.service.d/` drop-ins,
the app containers (`label=pantalan.app`, which listen only on loopback),
the `pantalan/*` images and the `pantalan` user. Running the install command
again brings it all back as it was: the same Admin and password, Admin
Token, certificate and apps, with no `--admin-user` needed.

```sh
sudo sh install.sh --purge
```

`--purge` does what `--uninstall` does and also deletes what it keeps: the
drop-ins, `/etc/pantalan/`, `/var/lib/pantalan/`, the app containers, the
`pantalan/*` images and the `pantalan` user. There's no undo, so it asks you
to type `purge` on the terminal. With no terminal it refuses unless you add
`--yes` (`--purge --yes`); `--yes` is accepted only with `--purge`.

Both delete Pantalan's `pantalan` ufw rules. They keep the `pantalan-ssh`
rules, and ufw as active as it was, because removing an SSH rule from an
active ufw locks out your next login; the summary lists them and how to
delete them (`sudo ufw status numbered`, then `sudo ufw delete N`).

Neither ever removes Docker, ufw or git, which other things on the server may
use. The install log, `/var/log/pantalan-install.log`, stays too.

### Verify a release

The installer checks the binary it downloads against the release's
`SHA256SUMS`, which ships in the same release. To check a release out of
band, with the [GitHub CLI](https://cli.github.com/), download its assets
and verify them against GitHub's signed attestations:

```sh
gh release download v1.2.3 --repo jimcru21/pantalan
sha256sum -c SHA256SUMS

# The asset is the one published in release v1.2.3 (releases are immutable:
# once published, neither the assets nor the tag can change).
gh release verify-asset v1.2.3 pantalan_v1.2.3_linux_amd64 --repo jimcru21/pantalan

# The binary was built by this repository's release workflow, from the tag.
gh attestation verify pantalan_v1.2.3_linux_amd64 --repo jimcru21/pantalan \
  --signer-workflow jimcru21/pantalan/.github/workflows/release.yml \
  --source-ref refs/tags/v1.2.3
```

Each release has four assets: `pantalan_vX.Y.Z_linux_amd64`,
`pantalan_vX.Y.Z_linux_arm64`, `install.sh` (with that version built in) and
`SHA256SUMS` over the other three. `gh release verify-asset` works for all
of them; the build provenance that `gh attestation verify` checks covers the
two binaries.
