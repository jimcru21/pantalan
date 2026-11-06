# Pantalan

A self-hosted platform for deploying apps from git, in the spirit of Coolify,
Dokploy and Tugport. It ships as one Go binary that runs next to Docker.

See [docs/architecture.md](docs/architecture.md) for the design.

## Requirements

- Go 1.26+
- Docker
- git (and `nixpacks` if you use `build_type: nixpacks`)

## Run locally

```sh
make run      # API + dashboard on :3000, app proxy on :8080
make check    # vet + race tests + gofmt
```

## Try it

```sh
TOKEN=dev

# Register an app (repo needs a Dockerfile at its root)
curl -s localhost:3000/api/v1/apps \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"hello","repo_url":"https://github.com/<you>/<repo>.git","domain":"hello.localhost","port":8080}'

# Deploy it (body optional: {"ref":"<branch|tag|sha>"})
curl -s -X POST localhost:3000/api/v1/apps/<app-id>/deployments -H "Authorization: Bearer $TOKEN"

# Watch status and logs
curl -s localhost:3000/api/v1/apps/<app-id>/deployments -H "Authorization: Bearer $TOKEN"
curl -s localhost:3000/api/v1/deployments/<deployment-id>/logs -H "Authorization: Bearer $TOKEN"

# Hit the app through the proxy
curl -s -H "Host: hello.localhost" localhost:8080
```

For push-to-deploy, point a GitHub webhook (content type `application/json`)
at `POST /webhooks/github` and use `PANTALAN_WEBHOOK_SECRET` as its secret.

## Configuration

| Variable | Default | |
|---|---|---|
| `PANTALAN_ADMIN_TOKEN` | *(required)* | Bearer token for `/api` |
| `PANTALAN_API_ADDR` | `:3000` | Dashboard + API |
| `PANTALAN_PROXY_ADDR` | `:80` | Public app traffic |
| `PANTALAN_DATA_DIR` | `/var/lib/pantalan` | Build checkouts and logs |
| `PANTALAN_WEBHOOK_SECRET` | *(empty: webhooks off)* | GitHub HMAC secret |
| `PANTALAN_WORKERS` | `2` | Concurrent deployments |

## Layout

```
cmd/pantalan/            entrypoint + wiring
internal/domain/        entities and rules
internal/service/       use cases + port interfaces
internal/httpapi/       REST API, webhooks, middleware
internal/store/memory/  in-memory repositories
internal/source/        git
internal/build/         dockerfile, nixpacks
internal/runtime/docker container lifecycle
internal/health/        readiness probe
internal/proxy/         reverse proxy
internal/queue/         deploy job queue
web/                    embedded dashboard
docs/                   architecture notes
```
