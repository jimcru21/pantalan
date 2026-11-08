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

## Packages

| Path | Role |
|---|---|
| `cmd/pantalan` | Entrypoint, config and dependency wiring |
| `internal/domain` | Entities (`App`, `Deployment`, `Admin`), validation, sentinel errors |
| `internal/service` | Use cases (`AppService`, `AdminService`, `Deployer`) and the port interfaces |
| `internal/httpapi` | REST API, GitHub webhook, auth middleware |
| `internal/dashtls` | Dashboard certificate (generate, load, renew) and the listener that redirects plain HTTP |
| `internal/password` | Argon2id hashing of the Admin password into a PHC string |
| `internal/store/sqlite` | App, deployment and Admin repositories and the sessions table in SQLite, plus the schema migrations |
| `internal/source` | Git checkout |
| `internal/build` | Dockerfile and Nixpacks builders |
| `internal/runtime/docker` | Container lifecycle through the docker CLI |
| `internal/health` | HTTP readiness probe |
| `internal/proxy` | Host-based reverse proxy |
| `internal/queue` | In-process job queue |
| `internal/execx` | Shared helpers for running external commands |
| `web` | Dashboard, embedded into the binary from `web/dist` |

## Decisions

- **Standard library first.** `net/http` routing, `log/slog` and
  `httputil.ReverseProxy` cover the scaffold. Add a dependency only when it
  replaces real code: `modernc.org/sqlite` and `golang.org/x/crypto` (Argon2id)
  so far.
- **Docker CLI rather than the Engine SDK.** It has no dependencies and its
  errors match what an operator sees. Move to the SDK when you need event
  streams or container stats.
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

- Apps and deployments survive a restart, but a deployment that was queued
  or mid-build when the service stopped is not picked up again.
- Containers publish on `127.0.0.1:<ephemeral>`. That port can change if
  Docker restarts the container. The production fix is a shared Docker network
  with the proxy reaching containers by name.
- Apps behind the proxy get no TLS yet (add ACME through autocert or
  certmagic in `proxy`). The Dashboard listener is HTTPS-only with a
  self-signed certificate; see "Dashboard listener" above.
- Auth is a single admin bearer token.
- Env values are stored in plaintext. Encrypt them at rest before real use.

## Where it grows

```
cmd/pantalan-agent/        node agent for multi-server (gRPC over WireGuard)
internal/proxy/acme.go    automatic HTTPS
internal/compose/         docker-compose stacks
internal/databases/       managed Postgres/MySQL/Redis templates
internal/backup/          scheduled dumps to S3-compatible storage
internal/auth/            users, sessions, scoped API keys
internal/events/          pub/sub for live logs and status over SSE
web/src/                  dashboard frontend source (builds to web/dist)
deploy/install.sh         curl | sh installer + systemd unit
```
