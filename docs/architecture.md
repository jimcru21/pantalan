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
     store/memory   source(git)   build   runtime/docker  health  proxy  │
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

## Packages

| Path | Role |
|---|---|
| `cmd/pantalan` | Entrypoint, config and dependency wiring |
| `internal/domain` | Entities (`App`, `Deployment`), validation, sentinel errors |
| `internal/service` | Use cases (`AppService`, `Deployer`) and the port interfaces |
| `internal/httpapi` | REST API, GitHub webhook, auth middleware |
| `internal/store/memory` | In-memory repositories (dev/test) |
| `internal/source` | Git checkout |
| `internal/build` | Dockerfile and Nixpacks builders |
| `internal/runtime/docker` | Container lifecycle through the docker CLI |
| `internal/health` | HTTP readiness probe |
| `internal/proxy` | Host-based reverse proxy |
| `internal/queue` | In-process job queue |
| `internal/execx` | Shared helpers for running external commands |
| `web` | Dashboard, embedded into the binary from `web/dist` |

## Decisions

- **Standard library only for now.** `net/http` routing, `log/slog` and
  `httputil.ReverseProxy` cover the scaffold. Add a dependency only when it
  replaces real code.
- **Docker CLI rather than the Engine SDK.** It has no dependencies and its
  errors match what an operator sees. Move to the SDK when you need event
  streams or container stats.
- **Built-in proxy rather than Traefik or Caddy.** There's no second process
  to configure and no label syncing; routes change atomically in memory.
  Coolify and Dokploy use Traefik, which is a valid alternative behind the
  same `Router` interface.
- **SQLite next.** `modernc.org/sqlite` is pure Go, so the binary stays a
  single static file.

## Known simplifications

These are in the scaffold on purpose and are listed here so nobody mistakes
them for design:

- State is in memory. Restarting loses apps and deployments.
- Containers publish on `127.0.0.1:<ephemeral>`. That port can change if
  Docker restarts the container. The production fix is a shared Docker network
  with the proxy reaching containers by name.
- There is no TLS yet (add ACME through autocert or certmagic in `proxy`).
- Auth is a single admin bearer token.
- Env values are stored in plaintext. Encrypt them at rest before real use.

## Where it grows

```
cmd/pantalan-agent/        node agent for multi-server (gRPC over WireGuard)
internal/store/sqlite/    persistent repositories + migrations/
internal/proxy/acme.go    automatic HTTPS
internal/compose/         docker-compose stacks
internal/databases/       managed Postgres/MySQL/Redis templates
internal/backup/          scheduled dumps to S3-compatible storage
internal/auth/            users, sessions, scoped API keys
internal/events/          pub/sub for live logs and status over SSE
web/src/                  dashboard frontend source (builds to web/dist)
deploy/install.sh         curl | sh installer + systemd unit
```
