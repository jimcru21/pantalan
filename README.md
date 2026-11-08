# Pantalan

A self-hosted platform for deploying apps from git, in the spirit of Coolify,
Dokploy and Tugport. It ships as one Go binary that runs next to Docker.

See [docs/architecture.md](docs/architecture.md) for the design.

## Install

On a fresh Debian 12, Debian 13, Ubuntu 24.04 LTS or Ubuntu 26.04 LTS
server (amd64 or arm64), as a user who can `sudo`:

```sh
curl -fsSL https://github.com/jimcru21/pantalan/releases/latest/download/install.sh | sudo sh
```

Flags go after `sh -s --`, for example for cloud-init or CI, where there's no
terminal to ask on:

```sh
curl -fsSL https://github.com/jimcru21/pantalan/releases/latest/download/install.sh \
  | sudo sh -s -- --admin-user alice --dashboard-port 8443
```

| Flag | |
|---|---|
| `--admin-user NAME` | The Admin's username. Asked for on the terminal when missing; with no terminal, the install stops and names the flag. |
| `--dashboard-port N` | The Dashboard's HTTPS port, default `8443`. |
| `--version vX.Y.Z` | Install that release instead of the one the script came with. |
| `--no-firewall` | Leave the firewall entirely alone, on this run and every re-run (stored as `PANTALAN_FIREWALL=off`). |

Once installed, the env file holds the settings: a re-run reads the
Dashboard port and firewall mode from it, and a flag that disagrees with it
stops the run. To change one, edit `/etc/pantalan/pantalan.env` and run
the installer again.

Running the command is the consent: there's no "continue?" prompt. The
installer first checks the host without changing anything (root, the
distro and architecture, systemd, Docker, and that ports 80, 443 and the
Dashboard port are free), then downloads the release binary and verifies it
against the release's `SHA256SUMS`, and checks the Admin username with it.
Only then does it change the host:

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
on the host, so that hardening is defence in depth, not a boundary.

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
adds no `DOCKER-USER` rules. What it does depends on the host:

| Host | What happens |
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
  container that doesn't.
- **Cloud security groups are separate.** A provider's firewall in front of
  the server knows nothing of ufw: open 80, 443 and the Dashboard port
  there too.

### Uninstall and purge

```sh
curl -fsSL https://github.com/jimcru21/pantalan/releases/latest/download/install.sh | sudo sh -s -- --uninstall
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
curl -fsSL https://github.com/jimcru21/pantalan/releases/latest/download/install.sh | sudo sh -s -- --purge
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

## Requirements for building from source

- Go 1.26+
- Docker
- git (and `nixpacks` if you use `build_type: nixpacks`)

## Run locally

```sh
make run      # Dashboard + API on https://localhost:8443, app proxy on :8080
make check    # vet + race tests + gofmt
```

### Test the installer

`deploy/installtest` runs `deploy/install.sh` end to end in privileged
systemd containers, one fresh container per path, against a fake release
built from your working tree. It needs Docker and network access to the
distro and Docker apt repositories, and takes a minute or two per distro.

```sh
make shellcheck                                 # lint the script (needs shellcheck)
make installtest                                # all four distros, on this machine's arch
INSTALLTEST_DISTROS=debian-12 make installtest  # one distro
```

## Try it

The Dashboard port speaks HTTPS with a self-signed certificate (see
[Dashboard certificate](#dashboard-certificate)), so these local examples
pass `-k`.

```sh
TOKEN=dev
API=https://localhost:8443/api/v1

# Register an app (repo needs a Dockerfile at its root)
curl -sk $API/apps \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"hello","repo_url":"https://github.com/<you>/<repo>.git","domain":"hello.localhost","port":8080}'

# Deploy it (body optional: {"ref":"<branch|tag|sha>"})
curl -sk -X POST $API/apps/<app-id>/deployments -H "Authorization: Bearer $TOKEN"

# Watch status and logs
curl -sk $API/apps/<app-id>/deployments -H "Authorization: Bearer $TOKEN"
curl -sk $API/deployments/<deployment-id>/logs -H "Authorization: Bearer $TOKEN"

# Hit the app through the proxy
curl -s -H "Host: hello.localhost" localhost:8080
```

### Push to deploy

Point a GitHub webhook (content type `application/json`) at
`https://<node-ip>:8443/webhooks/github` and use `PANTALAN_WEBHOOK_SECRET`
as its secret. GitHub can't verify the self-signed certificate, so untick
**SSL verification** on the hook. That only stops GitHub checking who it's
talking to: every payload is still signed with the secret, and Pantalan
rejects any whose HMAC signature doesn't match. The traffic stays encrypted.

## Dashboard certificate

The Dashboard, `/api/v1`, `/webhooks/github` and `/healthz` share one HTTPS
listener on `PANTALAN_DASHBOARD_ADDR`. TLS is always on. A plain `http://`
request to that port is redirected to the same URL over `https://`.

When `$PANTALAN_DATA_DIR/tls/cert.pem` or `key.pem` is missing, the service
creates a self-signed certificate at startup. It covers `127.0.0.1`, `::1`,
every global IP on the Node's interfaces at that moment, and the Node's
hostname, and is valid for 825 days. The service checks it at startup and
daily, and with fewer than 30 days left replaces it without a restart and
logs the new fingerprint. It is not regenerated when the Node's IPs change.

Your browser will warn that it doesn't know the certificate. Before you click
through, check that the fingerprint it shows is the Node's. On the server:

```sh
sudo pantalan tls fingerprint
# SHA256:AB:CD:...
```

The same value is in the service log at startup (`journalctl -u pantalan`).
From another machine you can read the fingerprint the port actually serves:

```sh
openssl s_client -connect <node-ip>:8443 </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256
```

If the two differ, something between you and the Node is presenting its own
certificate: don't sign in.

To rotate the certificate (after a key leak, or to cover a new IP):

```sh
sudo pantalan tls regenerate   # prints the new fingerprint
sudo systemctl restart pantalan
```

The app proxy is unaffected: it stays on port 80, and a request there for an
unknown host (including the Node's bare IP) gets a 404, never a redirect to
the Dashboard.

## Admin account

The Admin is the one account that signs in to the Dashboard. Its password is
always generated (26 base32 characters) and printed once; you can't choose
one. Run these on the server:

```sh
sudo pantalan admin create --username alice   # prints the password; refuses if an Admin exists
sudo pantalan admin show                      # prints the username; exits 3 when there is no Admin
sudo pantalan admin reset-password            # prints a new password
```

`reset-password` also signs out every Dashboard session and clears the
sign-in throttling, in one step, and works while the service is running.

Sign in at `https://<node-ip>:8443` with the username and the printed
password. A session lasts while you use it: it ends after an hour without a
request, and 24 hours after sign-in in any case. Up to 20 browsers can be
signed in at once; signing in a 21st ends the oldest session. After five
failed sign-ins, each further attempt has to wait, starting at a second and
doubling up to 15 minutes; the login page says how long, and
`reset-password` clears it. The Admin Token is for
`/api/v1` only and is refused by the Dashboard; a Dashboard session cookie
is no credential on `/api/v1`.
`admin create --username NAME --check` only validates the name and never
touches the database. Usernames are 1 to 32 characters of `a-z`, `0-9`, `.`,
`_` and `-`, starting with a letter, and are lowercased. The Admin can't be
renamed.

## Configuration

The service reads `PANTALAN_*` environment variables. An installed Node
keeps them in `/etc/pantalan/pantalan.env`; after editing it, run
`sudo systemctl restart pantalan`.

| Variable | Default | |
|---|---|---|
| `PANTALAN_ADMIN_TOKEN` | *(required)* | Bearer token for `/api` |
| `PANTALAN_DASHBOARD_ADDR` | `:8443` | HTTPS: Dashboard, API, webhooks, `/healthz` |
| `PANTALAN_PROXY_ADDR` | `:80` | Public app traffic |
| `PANTALAN_DATA_DIR` | `/var/lib/pantalan` | The database (`pantalan.db`), build checkouts, logs, `tls/` |
| `PANTALAN_WEBHOOK_SECRET` | *(empty: webhooks off)* | GitHub HMAC secret |
| `PANTALAN_WORKERS` | `2` | Concurrent deployments |
| `PANTALAN_FIREWALL` | `ufw` | Installer only, ignored by the service: `off` leaves the firewall alone (see [Firewall](#firewall)) |

## Layout

```
cmd/pantalan/            entrypoint + wiring
deploy/install.sh       the installer (a release asset)
deploy/installtest/     installer harness (build tag installtest)
internal/domain/        entities and rules
internal/service/       use cases + port interfaces
internal/httpapi/       REST API, webhooks, middleware
internal/dashtls/       Dashboard certificate + HTTPS listener
internal/dashboard/     Dashboard login, sessions and pages (html/template + htmx)
internal/store/sqlite/  SQLite repositories + migrations
internal/source/        git
internal/build/         dockerfile, nixpacks
internal/runtime/docker container lifecycle
internal/health/        readiness probe
internal/proxy/         reverse proxy
internal/queue/         deploy job queue
web/                    Dashboard templates and static files, embedded (no build step)
docs/                   architecture notes
```
