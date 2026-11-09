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
             │  │         │                                              │
             │  │         ▼ calls ports, never adapters                  │
             │  │  store/sqlite  source(git)  build  runtime/docker      │
             │  │  health  proxy (adapters: each implements one          │
             │  │  service/ports.go interface)                           │
             │  └──► nodeinfo (read-only Node information, through       │
             │       httpapi's own Host, Docker, Events interfaces)      │
             └──► dashboard (mounted at /: the embedded SPA from web,    │
                  behind the session gate for pages)                     │
                 └───────────────────────────────────────────────────────┘
```

Dependency rule: `domain` imports nothing internal. `service` imports only
`domain`. Adapters import `service` and `domain`. Only `cmd/pantalan` imports
everything. This keeps the deploy pipeline testable with fakes
(`service/deployer_test.go`) and lets each adapter be replaced on its own.

One exception, on purpose: the Node overview's reads skip the service
layer. `internal/httpapi` declares the small interfaces it reads through
(`NodeHost`, `NodeDocker`, `NodeEvents`) and imports `internal/nodeinfo`
for their value types (`Snapshot`, `DockerSummary`, `EventLog`, ...);
`cmd/pantalan` wires the `nodeinfo` implementations in, and `httpapi`
never builds one. There is no use case between the two:
no rule, no state and no write, only data to render, so a service port
would be a pass-through that re-declares every `nodeinfo` type. If a
read ever grows a rule (an alert, a decision a deployment acts on), it
moves into `service` behind a port.

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

Only then does `serve` ensure the TLS certificate, prune expired sessions
and start the daily maintenance tick, and last start the Node sampler, the
queue and the listeners.

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

The Dashboard is a single-page app in React, Vite and TypeScript
([ADR 0003](adr/0003-react-spa-dashboard.md)), with its source in
`web/app`. Everything it shows and does goes through `/api/v1`, the API
automation uses too, with the session cookie as its credential. React
Router routes it in the browser, TanStack Query fetches, caches and polls
(its intervals pause while the browser tab is hidden), Tailwind and
shadcn/ui components style it, and Recharts draws the sparklines.

- **Build and embedding.** `make web` (`vite build`) writes `index.html`
  and content-hashed files under `assets/` into `web/dist`. `web/dist` is
  gitignored except for a placeholder, and package `web` embeds it with
  `//go:embed all:dist`, so `go build` and `go test` work without Node:
  without an `index.html` the binary serves a "frontend not built, run
  `make web`" page in its place. `make build`, `deploy/release.sh` and CI
  build the frontend first, installing with `npm ci --ignore-scripts`.
- **Serving.** `internal/dashboard` is mounted at `/`, behind `/api/`,
  `/webhooks/` and `/healthz`. `/assets/<file>` is a hashed asset, served
  to anyone with `Cache-Control: public, max-age=31536000, immutable`; a
  missing one is a 404, never a page. Every other path is a page:
  - `/login` serves `index.html` to anyone. A visitor with a live session
    gets a 303 to `/`.
  - Any other page without a live session gets a 303 to
    `/login?next=<path and query>` before any app code loads, and a cookie
    naming an ended session is cleared. The SPA sends any API 401 to the
    same address, and sign-in goes on to `next` when it is a path on this
    origin.
  - A page with a live session gets `index.html`, and the SPA routes it,
    including its own "not found".
  - `index.html`, every `/api/` response and the redirects are `no-store`.
- **Headers.** Every Dashboard response carries
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`,
  `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`. So
  `index.html` has no inline script or style, there is no eval, and nothing
  may inject a `<style>` element, which rules out CSS-in-JS and shadcn/ui's
  chart wrapper. React's `style` prop is fine: it writes through the CSSOM.
- **API types.** The TypeScript types of the API's responses are generated
  from the Go structs with `tygo` (`make types`, configured in `tygo.yaml`)
  into `web/app/src/api/generated`, and CI fails when they are stale.
  The log streams' event names and `done` reasons are generated the same
  way (`internal/httpapi/stream_events.go`), so a rename on either side
  fails the SPA's type check.
- **Development.** `make web-dev`, or the compose stack's `web` service,
  runs Vite with hot reload and proxies `/api` to Pantalan. Vite serves
  `index.html` for every path, so the page gate and the CSP don't apply
  there.

Auth surfaces on the one listener:

| Route | Session cookie | Admin Token (bearer) | Other |
|---|---|---|---|
| Dashboard pages | `/login`: not needed; any other page: a live session, or a 303 to `/login?next=…` | no: a request with an `Authorization` header gets 401 | `/assets/…` needs nothing |
| `GET /api/v1/login-info`, `POST /api/v1/sessions`, `GET` and `DELETE /api/v1/sessions/current` | not needed (`GET …/sessions/current` checks it) | not needed | the sign-in contract; `POST` and `DELETE` are behind `http.CrossOriginProtection` whatever credential comes with them |
| the rest of `/api/v1/…` | yes, behind `http.CrossOriginProtection` (a cross-origin unsafe request gets 403) | yes, from any origin | an `Authorization` header is judged alone, whatever cookie comes with it; no credential, a wrong token or an ended session gets 401 |
| `/webhooks/github` | no | no | HMAC signature |
| `/healthz` | no | no | none needed |

- The sign-in screen talks to `internal/httpapi/sessions.go`, and every
  rule behind it is `service.AdminService`'s. `GET /api/v1/login-info`
  returns `hostname`, `address` (the `ip:port` the browser reached) and
  `admin_exists`. `POST /api/v1/sessions` takes `{"username", "password"}`
  and answers 204 with the session cookie, ending any session the browser
  already had, or 401 / 429 / 403 / 503 as below, with an `{"error": …}`
  body. A wrong username and a wrong password get the same 401 and
  message. With no Admin, the screen shows the `admin create` command
  instead of the form, and sign-in gets 403. `DELETE
  /api/v1/sessions/current` ends the browser's session, if any, and
  answers 204 clearing the cookie. `GET /api/v1/sessions/current` answers
  204 while the cookie names a live session and 401 once it doesn't, with
  `AdminService.CheckSession`, so checking never extends the session.
- `service.AdminService` issues a `crypto/rand.Text()` token per sign-in
  and stores only its SHA-256 hex in `sessions`. The cookie, which
  `internal/httpapi/cookie.go` sets, reads and clears, is
  `__Host-pantalan_session; Path=/; Secure; HttpOnly; SameSite=Strict`, with
  no expiry: the server ends a session after 1 h idle or 24 h in all.
  `last_seen_at` is rewritten at most once a minute. Nothing is cached in
  memory, so `admin reset-password` (which deletes every session) signs the
  browser out on its next request. Every page behind sign-in checks
  `GET /api/v1/sessions/current` every 30 s while its tab is shown, and
  again as soon as a hidden tab is shown, so an open page goes to sign-in
  within a minute of a reset even when it polls nothing (the env editor, a
  finished Deployment). The check doesn't extend the session. At most 20 sessions exist: a new
  sign-in evicts the oldest. Expired sessions are pruned at startup and on
  the daily maintenance tick that also checks TLS renewal.
- Sign-in throttling: one failure counter on the `admin` row, whatever
  username was submitted (unknown usernames verify against a dummy hash).
  Five failures are free; then the next attempt waits 1 s after the last
  failure, doubling to 15 min. An attempt inside the wait gets 429 with
  `Retry-After` before its password is checked, and isn't counted. A
  checked attempt is counted before its password is verified (the wait is
  checked and the count raised in one write transaction), and a success
  resets it, so racing attempts can't all be checked when a wait ends.
- At most 2 Argon2id hashes run at once; a sign-in that can't get a slot
  within 5 s gets 503 with `Retry-After: 5` and isn't counted. A hash
  whose PHC parameters differ from the current ones is replaced at the
  next successful sign-in; a sign-in that finds another sign-in's rehash
  in its place checks the password against it once more, rather than
  taking it for a reset.
- Log streams (`internal/httpapi/stream.go`) are Server-Sent Events over
  either credential. A stream opened with the cookie re-checks its
  session every 60 s with `AdminService.CheckSession`, which, unlike
  `Authenticate`, doesn't count as activity, so an open stream never keeps
  a session alive; it ends within a minute of a sign-out, an expiry or a
  reset-password, and the browser's reconnect then gets 401. Streams also
  end when the service shuts down. The dashboard listener has no write
  timeout, so a long stream is fine.

### Deployment Log

A Deployment's output, from the fetch to go-live or failure, is a file per
Deployment under `logs/` in the data dir, written by the Deployer.
`GET /api/v1/apps/{id}/deployments/{deployment-id}/log` serves all of it as
plain text; `…/log/stream` streams it:

- each event is a chunk of whole lines, and its `id` is the byte offset in
  the file after it, so a reconnect's `Last-Event-ID` resumes exactly;
- a first connect replays the last 1 MiB from a line start, after an
  `omitted` event (data and id: the bytes left out) when it skipped any;
- it follows the file (polled every 250 ms) while the Deployment runs,
  then sends `done` (data: the final status) and ends;
- if the Deployment is deleted with its App while the stream follows it,
  `done` says `deleted`: an event rather than a dropped connection, whose
  reconnect would get a 404 that `EventSource` can't tell from a 401.

ANSI escape codes are stripped and `\r` line ends become `\n` on the
server, in both forms. The older `GET /api/v1/deployments/{id}/logs`,
kept for scripts, serves the file byte for byte as written, as it always
has.

### App Log

What the live Deployment's container writes to stdout and stderr is the
App Log. `service.AppLogs` finds the App's live Deployment and follows its
container through the `service.ContainerLogs` port, which
`nodeinfo.Docker.FollowLogs` implements with the Engine API's
`containers/{id}/logs` (`follow`, `timestamps`, `tail=200` or `since`),
splitting its multiplexed stdout/stderr frames apart.
`GET /api/v1/apps/{id}/log/stream` streams it, ANSI stripped as above:

- each event is one line, stdout and stderr alike, and its `id` is
  `<deployment-id>@<unix-nanoseconds>` (Docker's timestamp of the line), so
  a reconnect's `Last-Event-ID` follows on with `since` from there;
- a first connect starts with the last 200 lines;
- when the container stops while its Deployment is still live, Docker
  restarts it and the stream follows it again (after 1 s, from the last
  line sent);
- once the Deployment isn't live any more (a new one replaced it) the
  stream sends `done` with data `replaced` and ends; a reconnect whose
  `Last-Event-ID` names an earlier Deployment gets the same;
- an App with no live Deployment gets `done` with data
  `no-live-deployment` at once. Both endings are events rather than HTTP
  errors, because `EventSource` can't read a status and the Dashboard
  takes a refused stream to mean the session ended.

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
  `images/json`, `version` and `system/df`, and follows
  `containers/{id}/logs` for the App Log. These are reads only:
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
  event. A drop before the ring holds any event leaves nothing to check
  the replay against, so the next event always gets the marker. A
  `/events` request that gets no response headers within 5 s is given up
  and retried like a drop; once the headers arrive the stream has no
  timeout.

`/api/v1` serves it as JSON, to the SPA and to automation alike, with raw
values (bytes, ratios where 0.25 is 25 %, counts, RFC 3339 times) that the
browser formats with `Intl`. The rules, such as the status level and its
reasons, are decided in Go (`internal/httpapi`). The wire types are in
`internal/httpapi/node_types.go`.

- `GET /api/v1/node`: the top bar and the stat band: `ip`, `arch`,
  `status` (`level` `ok`, `warn`, `down` or `off` before the first host
  reading, and `reasons`), `host` (missing until the sampler has a
  reading: hostname, OS, uptime, load, CPU and memory with the sampler's
  ring as `history`, and the disks), `host_error`, and `docker`
  (`reachable`, or the `error`, with the version and container counts).
  It answers while Docker is down, and never calls `system/df`.
- `GET /api/v1/node/containers`, `/images` and `/docker`: one per tab.
  While Docker is unreachable they answer 503 with
  `{"error": "Docker unreachable: …"}`. `system/df` is slow and grows with
  the Node's data, so the Docker tab's is shared between requests for 60 s
  after Docker computed it, by the handler's clock. Both response shapes
  are read: the per-kind summaries of API 1.52+ and the item lists of
  older daemons.
- `GET /api/v1/node/events`: the event ring, newest first, each event
  with `gap_before` where earlier events may be missing, and
  `stream_error` while the stream is down. It answers while Docker is
  down.

The page:

- The top bar's status dot is red when Docker is unreachable, amber when
  a shown disk or `MemAvailable` is under 10 % free, a container is
  unhealthy or restarting, or Docker reports `Warnings`, and green
  otherwise, with the reasons in its tooltip.
- The stat band has CPU and memory with sparklines of the sampler's ring
  that show the value at the point hovered, disk free/total for Docker's
  root dir (plus `/` when that is another filesystem, by statfs
  filesystem ID), the containers' running/total and unhealthy count, and
  the Docker version. The top bar and the band poll `GET /api/v1/node`
  every 5 s.
- Tabs: Containers (every container; `pantalan.app`-labelled ones badged
  with the App's name; running ones with CPU, a delta against that
  container's previous one-shot sample, missing the first time, and
  memory as usage − inactive file over the limit), Images (repo:tag,
  size, created, in use by any container), Docker (version, API version,
  storage driver, cgroup, root dir, `Warnings`, and `system/df`), and
  Events (the ring newest first, in UTC, with `die`'s exit code and
  `kill`'s signal, and a marker where events may be missing; it keeps
  showing while Docker is down, with a note that the stream is retrying).
  Only the open tab polls, because each tab's panel owns its query and
  unmounts while hidden: Containers and Events every 5 s, Images every
  30 s, Docker only when opened or refreshed.
- With Docker down the host tiles keep updating, the Docker tiles say
  "Docker unreachable" and every tab but Events shows one error card with a
  `systemctl status docker` hint; the open tab's next poll recovers.

## Installer

`deploy/install.sh` is a POSIX `sh` script (`set -eu`, ShellCheck-clean,
run by dash on the supported distros) shipped as a release asset, with
`DEFAULT_VERSION=@VERSION@` replaced by the tag at release time. Every
function is defined before the line that runs `main`, the last, and `main`
runs one function per step: preflight (read-only), download and verify, collect
inputs, packages, files, Admin, service, firewall, then the summary. The
firewall comes last, once the service answers: preflight classifies the
host (`classify_firewall`: mode from `PANTALAN_FIREWALL`, other firewall
managers, an active ufw, risks that make it stage rather than enable, and
the SSH ports as a union of sources) and `configure_firewall` reconciles
the `pantalan`-commented rules and adds `pantalan-ssh` ones: `plan_firewall`
works out the changes and the summary's Firewall line, `apply_firewall`
runs `ufw`. Each step is split into probe, decide and apply: the probes
are the only functions preflight's decisions ask the host with, and the
fixed host paths they read are prefixed with `HOST_ROOT`. Nothing on the
host changes before the packages step: collecting inputs asks the
downloaded binary's `admin show` whether an Admin exists, and `show` opens
the database read-only (`sqlite.OpenReadOnly`: no migration, no snapshot).
Each step converges, so a re-run
resumes, repairs or upgrades. Preflight's `compare_installed_version` puts
the target against the installed `pantalan version` with `sort -V`: newer
is an upgrade (the summary opens with `Upgraded vA → vB`), the same a
repair, and older is refused with how to restore a `pantalan.db.bak-*`
snapshot. What's missing is asked for on `/dev/tty` (the Admin username,
and the Dashboard port when the default can't be used); with no tty the
run stops and names the flag, so nothing is ever picked silently. Output
goes through `tee` into `/var/log/pantalan-install.log` except the Admin
password, which is printed after the log is closed.
`PANTALAN_RELEASE_BASE_URL` (test-only) points downloads at a fake release,
and `PANTALAN_INSTALL_NO_MAIN` (test-only) makes sourcing the script define
its functions and run nothing.

`--uninstall` and `--purge` take a separate, shorter path through `main`
(`remove_pantalan`): `--uninstall` removes only what an install puts back
(unit, binary), so a reinstall finds the kept database and Admin and asks
the downloaded binary about them; `--purge` also removes config, data,
drop-ins, app containers, `pantalan/*` images and the user, after `purge` is
typed at `/dev/tty` or `--yes` is given. Both delete the `pantalan` ufw
rules and keep the `pantalan-ssh` ones. Docker, ufw and git are never
removed.

The decisions have unit tests with no build tag, run by `make check`
(`deploy/installtest/install_unit_test.go`): the test sources the script
in dash with `PANTALAN_INSTALL_NO_MAIN` set, replaces the probes with stubs
and points `HOST_ROOT` at a temp dir, then calls one decision and checks
its output and exit status.

`deploy/installtest` (build tag `installtest`) tests it end to end: Go tests
drive a `Substrate` that boots a fresh Node per path, run the script as
`curl | sh` against a fake release built from the working tree, and check
the result from outside the Node with shared post-install assertions.
`ci.yml` runs it on every PR in privileged systemd containers (per-distro
images in `deploy/installtest/images/`) on Debian 12/13 and Ubuntu
24.04/26.04, on amd64 and arm64. `installer-vm.yml` runs the VM-only paths
(reboot, `IPV6=no` staging, snap Docker, `ssh.socket`, upgrade from the
previous published release, and the install → reboot → upgrade →
`--uninstall` → `--purge` sequence) on x64 KVM VMs from the official cloud
images, reached over SSH on libvirt's `virbr0`: nightly, by hand, and as
`release.yml`'s gate on the exact assets it built.

Releases come from `release.yml` on a `vX.Y.Z` tag push, any other tag
failing first. It runs `ci.yml` as its gate, builds the assets with
`deploy/release.sh` (the same script the harness builds its fake release
with, putting a stand-in `npm` on its `PATH` so it needs no Node) in a job
with read-only access; the script builds the Dashboard's frontend with
`npm ci --ignore-scripts` before the binaries that embed it. The workflow
then runs the VM tier on the assets, and hands them as a workflow artifact to the one job with write access (which needs
the VM tier, so a failed gate leaves no draft), which runs no repository code: it checks
the assets, attests the binaries' provenance, creates a draft release,
uploads and publishes last.

## Packages

| Path | Role |
|---|---|
| `cmd/pantalan` | Entrypoint, config and dependency wiring |
| `internal/domain` | Entities (`App`, `Deployment`, `Admin`), validation, sentinel errors |
| `internal/service` | Use cases (`AppService`, `AdminService`, `Deployer`) and the port interfaces; `AdminService` owns every rule about the Admin, for both the Dashboard and `pantalan admin` (`admin show` only reads the store) |
| `internal/httpapi` | REST API (Admin Token or session cookie), sign-in and the session cookie, the Node overview's JSON, GitHub webhook, `/healthz`; mounts the Dashboard at `/` |
| `internal/dashtls` | Dashboard certificate (generate, load, renew) and the listener that redirects plain HTTP |
| `internal/password` | Argon2id hashing of the Admin password into a PHC string |
| `internal/store/sqlite` | App and deployment repositories and the Admin's credentials (Admin and sessions) in SQLite, plus the schema migrations |
| `internal/source` | Git checkout |
| `internal/build` | Dockerfile builder; the Nixpacks builder fails with "nixpacks not installed on this Node" unless a hand-built setup has nixpacks on the PATH |
| `internal/runtime/docker` | Container lifecycle through the docker CLI |
| `internal/health` | HTTP readiness probe |
| `internal/proxy` | Host-based reverse proxy |
| `internal/queue` | In-process job queue |
| `internal/execx` | Shared helpers for running external commands |
| `internal/dashboard` | Serves the embedded SPA: `index.html` for every page behind the session gate, the hashed assets, the "frontend not built" page, and the security and cache headers |
| `internal/nodeinfo` | Read-only Node information: host health from `/proc` and `statfs`, the background sampler's ring, Docker through the Engine API on its unix socket, and the container event ring fed by its `/events` stream |
| `web` | The Dashboard SPA: its source in `web/app` (React, Vite, TypeScript) and the build in `web/dist`, which package `web` embeds into the binary |

## Decisions

- **Standard library first.** `net/http` routing, `log/slog` and
  `httputil.ReverseProxy` cover the scaffold. Add a dependency only when it
  replaces real code: `modernc.org/sqlite` and `golang.org/x/crypto` (Argon2id)
  so far. `tygo`, which generates the API's TypeScript types, is a Go
  `tool` dependency, so the binary doesn't depend on it.
- **The Dashboard is a React SPA over the one `/api/v1`**
  ([ADR 0003](adr/0003-react-spa-dashboard.md), superseding the
  server-rendered htmx Dashboard of [ADR 0002](adr/0002-server-rendered-dashboard-with-htmx.md)).
  Pages change without a reload, a log keeps streaming across pages, and
  charts answer hover, while there is still one API: anything the
  Dashboard does, automation can do with the Admin Token. The build is
  embedded, so the binary stays one file; `go build` without Node still
  works and says the frontend isn't built. Sign-in needs JavaScript.
- **A small, locked frontend supply chain.** npm installs from the
  committed lockfile with `npm ci --ignore-scripts` in CI and the release,
  so no package runs code at install; Dependabot groups npm updates into
  one pull request a week; and a new runtime dependency's pull request
  says why it's needed (dev tooling is exempt). This is the frontend's
  "standard library first".
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
