# Architecture

Pantalan is a self-hosted PaaS in the same family as Coolify, Dokploy and
Tugport: push to git, get a running container behind a domain.

## Shape: modular monolith, ports & adapters

One `pantalan` binary runs the dashboard, REST API, deploy workers and the
reverse proxy. Installing it means copying one file and adding a systemd unit,
the same model as Tugport.

Inside, the code follows ports & adapters (hexagonal):

```
                 ┌──────────────── cmd/pantalan (wiring) ─────────────────┐
                 │                                                       │
  HTTP ──► httpapi ──► service (use cases) ◄── ports.go: interfaces      │
     └───► dashboard ───────┘                                            │
                          │                                              │
                          ▼ calls ports, never adapters                  │
     store/sqlite   source(git)   build   runtime/docker  health  proxy  │
     (adapters: each implements one interface from service/ports.go)     │
                 └───────────────────────────────────────────────────────┘
```

Dependency rule: `domain` imports nothing internal. `service` imports only
`domain`. Adapters import `service` and `domain`. Only `cmd/pantalan` imports
everything. This keeps the deploy pipeline testable with fakes
(`service/deployer_test.go`) and lets each adapter be replaced on its own.

## Deploy pipeline

`service.Deployer` runs a zero-downtime (blue-green) swap:

1. **queued**: `Start` records the deployment and enqueues it.
2. **building**: fetch the exact ref into `$DATA/builds/<id>`, then build an image.
3. **starting**: run the new container next to the old one.
4. Health-check the new container. On failure, remove it. The old one keeps serving.
5. **live**: switch the proxy route, then mark the previous deployment
   **superseded** and remove its container.

Deployments of the same app are serialised. Different apps build in parallel,
up to `PANTALAN_WORKERS` at a time.

### Startup recovery

`Deployer.Recover` runs once at startup, after the store is open and before
the queue starts, so a restart never leaves a deployment stuck:

- **building** and **starting** deployments are marked **failed** with
  `interrupted by restart`; the container is removed (by its
  `pantalan-<app>-<deployment>` name, since the ID is recorded only once the
  run finishes) and `$DATA/builds/<id>` is deleted.
- **queued** deployments are re-enqueued, oldest `created_at` first.
- Each **live** deployment's upstream is re-read from Docker
  (`Runtime.Upstream`, i.e. `docker port`), saved if it changed, and its
  route restored. If Docker can't say, the stored upstream is kept.

## Dashboard listener

One HTTPS listener on `PANTALAN_DASHBOARD_ADDR` (default `:8443`) serves the
Dashboard, `/api/v1`, `/webhooks/github` and `/healthz`. The app proxy keeps
its own plain-HTTP listener on `PANTALAN_PROXY_ADDR` (`:80`), and an unknown
host there, the Node's bare IP included, gets a 404.

- `dashtls.RedirectPlainHTTP` peeks at each connection's first byte: a TLS
  handshake (`0x16`) goes on to `http.Server.ServeTLS`, anything else is
  answered with a 308 to `https://<host>:<port><path>`. No HSTS.
- The certificate is a self-signed ECDSA P-256 leaf in `$DATA/tls/`
  (`cert.pem`, `key.pem` at `0600`), written atomically. `serve` generates it
  when missing and serves it through `GetCertificate` from an atomic
  pointer, so renewal (checked at startup and daily, with fewer than 30 days
  left) swaps it without a restart. `pantalan tls regenerate` only rewrites
  the files; the service picks them up on restart.
- The fingerprint (`SHA256:AB:CD:…` over the DER) is how the Admin tells the
  real certificate from a man in the middle: `pantalan tls fingerprint`, and
  the service log at startup and renewal.

## Dashboard

The Dashboard is server-rendered ([ADR 0002](adr/0002-server-rendered-dashboard-with-htmx.md)):
`internal/dashboard` renders Go `html/template` files from `web/templates`
and serves `web/static` (CSS, `app.js` and htmx 2.0.11, vendored as one
minified file with its licence). Both are embedded with `go:embed`; there is
no Node toolchain and no build step. `dashboard.New` parses every template
once, so a broken template fails startup. The handlers call the service
layer directly, never `/api/v1`.

Auth surfaces on the one listener:

| Route | Credential |
|---|---|
| Dashboard pages, `POST /login`, `POST /logout` | session cookie only, behind `http.CrossOriginProtection`; a request with an `Authorization` header gets 401 |
| `/api/v1/…` | Admin Token (bearer) only; cookies are ignored |
| `/webhooks/github` | HMAC signature |
| `/healthz` | none |

- Login and logout are plain `<form method="post">` answered with 303
  redirects. A wrong username and a wrong password get the same 401 and
  message. With no Admin, the login card shows the `admin create` command
  instead of the form, and `POST /login` gets 403.
- `service.AuthService` issues a `crypto/rand.Text()` token per login and
  stores only its SHA-256 hex in `sessions`. The cookie is
  `__Host-pantalan_session; Path=/; Secure; HttpOnly; SameSite=Strict`, with
  no expiry: the server ends a session after 1 h idle or 24 h in all.
  `last_seen_at` is rewritten at most once a minute. Nothing is cached in
  memory, so `admin reset-password` (which deletes every session) signs the
  browser out on its next request. At most 20 sessions exist: a new login
  evicts the oldest. Expired sessions are pruned at startup and on the
  daily maintenance tick that also checks TLS renewal.
- Login throttling: one failure counter on the `admin` row, whatever
  username was submitted (unknown usernames verify against a dummy hash).
  Five failures are free; then the next attempt waits 1 s after the last
  failure, doubling to 15 min. An attempt inside the wait gets 429 with
  `Retry-After` before its password is checked, and isn't counted. A
  checked attempt is counted before its password is verified (a
  compare-and-swap on the count), and a success resets it, so racing
  attempts can't all be checked when a wait ends.
- At most 2 Argon2id hashes run at once; a login that can't get a slot
  within 5 s gets 503 and isn't counted. A hash whose PHC parameters differ
  from the current ones is replaced at the next successful login.
- Every Dashboard response carries
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`,
  so templates have no inline scripts or `style=` attributes and htmx runs
  with `allowEval=false` and `includeIndicatorStyles=false`.
- Static files live under `/static/<version>/` with
  `Cache-Control: public, max-age=31536000, immutable`. `<version>` is the
  release tag, else the short VCS revision, else (binaries with no VCS
  stamp, such as `go run`) a digest of the files. Pages, fragments and
  redirects are `no-store`.

### Node overview

The Dashboard's home page shows the Node's health, read by
`internal/nodeinfo`, stdlib only and nothing persisted:

- **Host**: `/proc` (`stat`, `meminfo`, `loadavg`, `uptime`,
  `sys/kernel/*`) and `statfs`. A background sampler reads it every 5 s
  into a 120-sample in-memory ring (10 minutes) that feeds the sparklines
  and the CPU delta.
- **Docker**: the Engine API on the unix socket (`nodeinfo.Docker`,
  `net/http` with a unix dialer, no SDK), read on demand per request. Each
  read starts with `/_ping`, whose `Api-Version` is the version the
  following calls use, capped at `nodeinfo.MaxAPIVersion`; a failed ping
  is "Docker unreachable". It uses `info`, `containers/json`, one-shot
  `containers/{id}/stats` (at most 8 in flight across all requests),
  `images/json`, `version` and `system/df`. These are reads only:
  container lifecycle for deployments stays on the docker CLI (see
  Decisions).
- **Docker events**: `nodeinfo.EventStream` holds one `/events` stream
  open, filtered at the source to container `start`, `stop`, `die`,
  `oom`, `kill`, `restart`, `health_status` and `destroy`, into a
  100-event in-memory ring. A dropped stream reconnects after 1 s,
  doubling to 30 s while attempts fail and back to 1 s once one connects,
  and backfills with `since=<last event time>` (or the time it last
  connected, before any event). dockerd replays its buffer from `since`
  inclusive, so a replay that starts with the ring's last event proves
  the gap was covered; one that doesn't (the buffer moved past it, or
  dockerd restarted) puts a "stream reconnected" marker before the next
  event.

The page:

- The top bar's status dot is red when Docker is unreachable, amber when
  a shown disk or `MemAvailable` is under 10 % free, a container is
  unhealthy or restarting, or Docker reports `Warnings`, and green
  otherwise, with the reasons in its tooltip.
- The stat band has CPU and memory with server-drawn SVG sparklines, disk
  free/total as a `<meter>` for Docker's root dir (plus `/` when that is
  another filesystem, by statfs filesystem ID), the containers'
  running/total and unhealthy count, and the Docker version. It never
  calls `system/df`.
- Tabs: Containers (every container; `pantalan.app`-labelled ones badged
  with the app's name; running ones with CPU %, a delta against that
  container's previous one-shot sample, "–" the first time, and memory as
  usage − inactive file over the limit), Images (repo:tag, size, created,
  in use by any container), and Docker (version, API version, storage
  driver, cgroup, root dir, `Warnings`, and `system/df`), and Events (the
  ring newest first, in UTC, with `die`'s exit code and `kill`'s signal;
  it keeps showing while Docker is down, with a note that the stream is
  retrying). Each tab is a block and fragment that renders the tab bar
  too, so a tab button swaps the whole section and only the open tab
  polls: Containers and Events every 5 s, Images every 30 s, Docker not
  at all (it has a Refresh button).
  `system/df` is slow and grows with the Node's data, so its result is
  shared between requests for 60 s, by the Node clock. Both response
  shapes are read: the per-kind summaries of API 1.52+ and the item lists
  of older daemons.
- With Docker down the host tiles keep updating, the Docker tiles say
  "Docker unreachable" and every tab but Events shows one error card with a
  `systemctl status docker` hint; the open tab's next poll recovers.
- Each polled region (the top bar, the stat band, the open tab) is a
  template block in `web/templates/pages/overview.html` and its own
  route, `GET /fragments/<block>`, which renders the same block. The
  region's element carries `hx-get`, a plain `hx-trigger="every 5s"` and
  `hx-swap="outerHTML"`, so each fresh copy keeps polling.
- `app.js` cancels `htmx:beforeRequest` while the tab is hidden and
  refreshes every polled region once when it is shown again.
- A fragment request without a live session gets 401 with
  `HX-Redirect: /login`, which htmx turns into a full navigation.

## Installer

`deploy/install.sh` is a POSIX `sh` script (`set -eu`, ShellCheck-clean,
run by dash on the supported distros) shipped as a release asset, with
`DEFAULT_VERSION=@VERSION@` replaced by the tag at release time. Every
function is defined before `main "$@"`, the last line, and `main` runs one
function per step: preflight (read-only), download and verify, collect
inputs, packages, files, Admin, service, then the summary. Nothing on the
host changes before the packages step; each step converges, so a re-run
resumes. Output goes through `tee` into `/var/log/pantalan-install.log`
except the Admin password, which is printed after the log is closed.
`PANTALAN_RELEASE_BASE_URL` (test-only) points downloads at a fake release.

`--uninstall` and `--purge` take a separate, shorter path through `main`
(`remove_pantalan`): `--uninstall` removes only what an install puts back
(unit, binary), so a reinstall finds the kept database and Admin and asks
the downloaded binary about them; `--purge` also removes config, data,
drop-ins, app containers, `pantalan/*` images and the user, after `purge` is
typed at `/dev/tty` or `--yes` is given. Docker, ufw and git are never
removed.

`deploy/installtest` (build tag `installtest`) tests it end to end: Go tests
drive a `Substrate` (privileged systemd containers on PRs; VMs later) that
boots a fresh Node per path from per-distro images in
`deploy/installtest/images/`, run the script as `curl | sh` against a fake
release built from the working tree, and check the result from outside the
Node with shared post-install assertions. `ci.yml` runs it on Debian 12/13
and Ubuntu 24.04/26.04, on amd64 and arm64.

Releases come from `release.yml` on a `vX.Y.Z` tag push, any other tag
failing first. It runs `ci.yml` as its gate, builds the assets with
`deploy/release.sh` (the same script the harness builds its fake release
with) in a job with read-only access, and hands them as a workflow artifact
to the one job with write access, which runs no repository code: it checks
the assets, attests the binaries' provenance, creates a draft release,
uploads and publishes last.

## Packages

| Path | Role |
|---|---|
| `cmd/pantalan` | Entrypoint, config and dependency wiring |
| `internal/domain` | Entities (`App`, `Deployment`, `Admin`), validation, sentinel errors |
| `internal/service` | Use cases (`AppService`, `AdminService`, `AuthService`, `Deployer`) and the port interfaces |
| `internal/httpapi` | REST API (Admin Token), GitHub webhook, `/healthz`; mounts the Dashboard at `/` |
| `internal/dashtls` | Dashboard certificate (generate, load, renew) and the listener that redirects plain HTTP |
| `internal/password` | Argon2id hashing of the Admin password into a PHC string |
| `internal/store/sqlite` | App, deployment, Admin and session repositories in SQLite, plus the schema migrations |
| `internal/source` | Git checkout |
| `internal/build` | Dockerfile and Nixpacks builders |
| `internal/runtime/docker` | Container lifecycle through the docker CLI |
| `internal/health` | HTTP readiness probe |
| `internal/proxy` | Host-based reverse proxy |
| `internal/queue` | In-process job queue |
| `internal/execx` | Shared helpers for running external commands |
| `internal/dashboard` | Dashboard handlers: login, logout, session middleware, pages and their fragments; security and cache headers |
| `internal/nodeinfo` | Read-only Node information: host health from `/proc` and `statfs`, the background sampler's ring, Docker through the Engine API on its unix socket, and the container event ring fed by its `/events` stream |
| `web` | Dashboard assets only (templates and static files), embedded into the binary |

## Decisions

- **Standard library first.** `net/http` routing, `log/slog` and
  `httputil.ReverseProxy` cover the scaffold. Add a dependency only when it
  replaces real code: `modernc.org/sqlite` and `golang.org/x/crypto` (Argon2id)
  so far.
- **Docker CLI for container lifecycle, the Engine API for reads.**
  Deployments build, run, look up ports and remove containers through the
  docker CLI (`internal/runtime/docker`): it has no dependencies and its
  errors match what an operator sees. The Node overview's reads (container
  list, one-shot stats, images, info, version, `system/df` and the event
  stream) go through the Engine API on the unix socket with `net/http`
  instead (`internal/nodeinfo`): raw numbers rather than display strings,
  one-shot stats in milliseconds instead of a 2 s CLI call, and a real
  event stream. No SDK: the API version is negotiated from `/_ping`.
- **Built-in proxy rather than Traefik or Caddy.** There's no second process
  to configure and no label syncing; routes change atomically in memory.
  Coolify and Dokploy use Traefik, which is a valid alternative behind the
  same `Router` interface.
- **One SQLite file for all state** ([ADR 0001](adr/0001-single-sqlite-file-shared-by-service-and-cli.md)).
  `internal/store/sqlite` keeps apps and deployments in
  `$PANTALAN_DATA_DIR/pantalan.db` (`0600`, WAL) through `modernc.org/sqlite`,
  which is pure Go, so the binary stays a single static file. Every process
  that opens the file migrates it first: the numbered `.sql` files in
  `internal/store/sqlite/migrations` are embedded, forward-only and applied
  in one transaction, with the schema version in `PRAGMA user_version`. When
  migrations are pending the old database is copied to
  `pantalan.db.bak-<version>` first (the two newest copies are kept), and a
  database newer than the binary is refused. Tests use the same store on a
  temp directory; there is no in-memory store.

## Known simplifications

These are in the scaffold on purpose and are listed here so nobody mistakes
them for design:

- Containers publish on `127.0.0.1:<ephemeral>`. That port can change if
  Docker restarts the container; upstreams are refreshed only at startup.
  The production fix is a shared Docker network with the proxy reaching
  containers by name.
- Apps behind the proxy get no TLS yet (add ACME through autocert or
  certmagic in `proxy`). The Dashboard listener is HTTPS-only with a
  self-signed certificate; see "Dashboard listener" above.
- One Admin and one shared Admin Token: no other users, roles or scoped API keys.
- Env values are stored in plaintext. Encrypt them at rest before real use.

## Where it grows

```
cmd/pantalan-agent/        node agent for multi-server (gRPC over WireGuard)
internal/proxy/acme.go    automatic HTTPS
internal/compose/         docker-compose stacks
internal/databases/       managed Postgres/MySQL/Redis templates
internal/backup/          scheduled dumps to S3-compatible storage
internal/auth/            more users, roles, scoped API keys
internal/events/          pub/sub for live logs and status over SSE
```
