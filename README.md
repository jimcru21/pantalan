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
make run      # Dashboard + API on https://localhost:8443, app proxy on :8080
make check    # vet + race tests + gofmt
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

## Configuration

| Variable | Default | |
|---|---|---|
| `PANTALAN_ADMIN_TOKEN` | *(required)* | Bearer token for `/api` |
| `PANTALAN_DASHBOARD_ADDR` | `:8443` | HTTPS: Dashboard, API, webhooks, `/healthz` |
| `PANTALAN_PROXY_ADDR` | `:80` | Public app traffic |
| `PANTALAN_DATA_DIR` | `/var/lib/pantalan` | The database (`pantalan.db`), build checkouts, logs, `tls/` |
| `PANTALAN_WEBHOOK_SECRET` | *(empty: webhooks off)* | GitHub HMAC secret |
| `PANTALAN_WORKERS` | `2` | Concurrent deployments |

## Layout

```
cmd/pantalan/            entrypoint + wiring
internal/domain/        entities and rules
internal/service/       use cases + port interfaces
internal/httpapi/       REST API, webhooks, middleware
internal/dashtls/       Dashboard certificate + HTTPS listener
internal/store/sqlite/  SQLite repositories + migrations
internal/source/        git
internal/build/         dockerfile, nixpacks
internal/runtime/docker container lifecycle
internal/health/        readiness probe
internal/proxy/         reverse proxy
internal/queue/         deploy job queue
web/                    embedded dashboard
docs/                   architecture notes
```
