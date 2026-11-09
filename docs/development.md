# Development

How to run Pantalan from source, test the installer and cut a release.
New here? The [Contributing](../README.md#-contributing) section of
the README has the two-minute version.

## Requirements for building from source

- Go 1.26+
- Docker
- git
- Node 24.22.2 with npm (the version in `web/app/.nvmrc`), for the
  Dashboard SPA in `web/app` and for `make check`. `go build` and `go test`
  don't need it, so Go-only work never waits on Node.

Apps build from the Dockerfile at the root of their repository. Nixpacks
isn't part of a Node: the installer doesn't install it, so an app with
`build_type: nixpacks` fails to build with "nixpacks not installed on this
Node".

## Run locally

### Set up a fresh VPS for development

[dev/setup-vps.sh](../dev/setup-vps.sh) gets a fresh Debian or Ubuntu server
(amd64 or arm64) ready for both ways of developing below. It installs Go
(the newest patch release of the version in `go.mod`, checksum-verified,
in `/usr/local/go`), Docker CE with the buildx and Compose plugins from
Docker's apt repository, git, make, gcc (for `go test -race`), shellcheck,
jq and the GitHub CLI. It also adds you to the `docker` group.

For working on the repo with Claude Code, it also installs, in your home:

- [Claude Code](https://code.claude.com/docs/en/setup), with its native
  installer (it updates itself).
- [colgrep](https://github.com/lightonai/next-plaid/tree/main/colgrep),
  semantic code search, with its Claude Code hooks.
- [semble](https://github.com/MinishLab/semble), code search, installed
  with uv and set up as a Claude Code MCP server and sub-agent.
- [Matt Pocock's skills](https://github.com/mattpocock/skills), from a
  checkout of their newest release in `~/.local/share/mattpocock-skills`,
  linked into `~/.claude/skills`. This isn't the Claude Code plugin, which
  trails the releases. Their per-repo setup is already in
  [docs/agents](agents).

Run it as your normal user with sudo, so that user is the one it sets up:

```sh
curl -fL --max-time 120 -o setup-vps.sh https://raw.githubusercontent.com/jimcru21/pantalan/main/dev/setup-vps.sh
sudo sh setup-vps.sh --clone ~/pantalan
```

or `sudo sh dev/setup-vps.sh` from a checkout. `--user NAME` sets up a
user other than the one who ran sudo, `--go-version 1.26.1` pins a Go
release, and `--no-agent-tools` skips Claude Code and its tools. Run it
again any time: it moves Go, colgrep, semble and the skills to their newest
releases and leaves everything else as it is. It never touches the
firewall, and it refuses to replace a Docker that isn't Docker CE
(`docker.io`, snap). Then log out and back in so the `docker` group and
the new `PATH` apply, and run `claude` to sign in.

### Develop with Docker

The easiest way: all you need is Docker with Compose (no Go, git or
anything else on your machine).

```sh
docker compose up
```

The first start builds the dev image, then prints the Dashboard URL, the
dev Admin (`dev`) and its password (shown once) and the Admin Token. Sign
in at https://localhost:5173 and accept the browser's warning about the
self-signed certificate. That is the Dashboard, the React SPA in `web/app`
(ADR 0003), on Vite's dev server with Pantalan's certificate and `/api`
proxied to Pantalan on https://localhost:8443. Edits under `web/app/src`
show up in the open page at once (hot reload), with no refresh. Edit any
`.go` or migration file and Pantalan rebuilds and restarts within seconds
([air](https://github.com/air-verse/air) watches the checkout). A change
that doesn't compile prints the error and keeps the last good build
running.

8443 itself serves the Dashboard embedded in the binary, which is only
there once `make web` has built it into `web/dist` (air then rebuilds);
until then it serves a "frontend not built" page. Vite serves the SPA for
every path and doesn't send the CSP, so the page gate and the CSP are
only checked against the built binary.

Apps you deploy run in the stack's own Docker daemon (Docker-in-Docker, the
`docker` service), not in your machine's Docker, and are reachable through
the app proxy at http://localhost:8080 with their domain as the `Host`
header (`curl -H 'Host: myapp.example.com' http://localhost:8080`).

```sh
docker compose up -d                                             # in the background
docker compose logs -f pantalan                                  # follow the build and server output
docker compose logs -f web                                       # the SPA dev server's output
docker compose exec pantalan pantalan admin reset-password       # a new Admin password
docker compose exec pantalan cat /data/dev-secrets.env           # the Admin Token and webhook secret
docker compose down                                              # stop (data, apps and caches stay)
docker compose down -v                                           # stop and wipe everything: a fresh start
```

The stack's settings go in a `.env` file next to `compose.yaml` (ignored by
git). [.env.example](../.env.example) lists them all with their defaults:

```sh
cp .env.example .env
```

**On a VPS**, to open the Dashboard from your own browser, make the ports
listen on every address in `.env`:

```sh
PANTALAN_DEV_BIND=0.0.0.0
PANTALAN_DEV_HOST=203.0.113.10   # the VPS's public IP, for the printed URLs
```

then `docker compose up -d` and open `https://<public-ip>:5173` (the
API is on `https://<public-ip>:8443`). Docker's published ports bypass
ufw, so the provider's firewall or security group is what decides who can
reach them: open 8443 and 5173 (and 8080 to try the app proxy), ideally
only to your own IP. The Admin Token and webhook secret are random,
made on first start and kept in the data volume, because anyone who can
reach the API with the token can deploy containers on the VPS.

The `docker` service runs privileged, which Docker-in-Docker needs.

### Without Docker

With Go 1.26+, git and Docker on your machine (and Node for the SPA):

```sh
make run      # API (and the Dashboard built by make web) on https://localhost:8443, app proxy on :8080
make web-dev  # the SPA with hot reload on https://localhost:5173, /api proxied to make run
make check    # Go: vet, race tests, gofmt; SPA: type check, ESLint, Prettier, tests
```

`make run` doesn't rebuild on changes: stop it and run it again.
`make web-dev` uses the certificate `make run` keeps in `./data/tls`, so
start `make run` first.

### The Dashboard SPA

The Dashboard's frontend is a React, Vite and TypeScript SPA in `web/app`
([ADR 0003](adr/0003-react-spa-dashboard.md)), styled with Tailwind v4 and
shadcn/ui (`components.json`; add a component with
`npx shadcn@latest add <name>` in `web/app`). Pages are routed by React
Router and fetch through TanStack Query; log streams use `EventSource`.
The CSP forbids `<style>` elements, so nothing may inject one: no CSS-in-JS,
no shadcn/ui chart wrapper. React's `style` prop is fine.

```sh
make web-deps   # npm ci --ignore-scripts, from the lockfile (make does it when needed)
make web-check  # tsc, ESLint, Prettier and the tests (part of make check)
make web        # build into web/dist, which the binary embeds
make build      # make web, then go build: bin/pantalan with the SPA embedded
make types      # regenerate the API's TypeScript types from the Go structs
```

In `web/app`, `npm run format` fixes formatting and `npx vitest` runs the
tests in watch mode.

**Tests** render the whole app at a route with Vitest and React Testing
Library, and stub only `fetch` and `EventSource`
(`src/test/render.tsx` and `src/test/network.ts`). No hook or component is
mocked. A request no test handler answers fails the test. Fixtures are
typed by the generated API types (`src/test/fixtures.ts`).

**Smoke tests** in `web/app/e2e` drive Chromium with
[Playwright](https://playwright.dev) against the binary built with its
embedded frontend. They're the only tests where the embedding, the session
cookie, the cross-origin check and the CSP work together. The suite starts
`bin/pantalan` itself, on free loopback ports with a fresh data dir and an
Admin made by `pantalan admin create`, and stops it at the end. Every test
fails if the browser's console reports a CSP violation. CI runs them in
the `smoke` job; they aren't part of `make check`.

```sh
make smoke-docker   # make build, then the suite in Playwright's Docker image
make smoke          # on this machine: make build, then npm run smoke in web/app
```

`make smoke` needs Chromium and its system libraries, installed once with
`npx playwright install --with-deps chromium` in `web/app`. Without them,
use `make smoke-docker`, which runs the suite in
`mcr.microsoft.com/playwright` at the version in `package.json`, as your
own user. With the binary already built, `npm run smoke -- --headed` (or
`--ui`, `-g <test name>`) in `web/app` runs it again. `PANTALAN_BIN` points
it at another binary. A failed test keeps a trace in `web/app/test-results`:
open it with `npx playwright show-trace <trace.zip>`.

**API types** in `web/app/src/api/generated` are generated from the Go
structs by [tygo](https://github.com/gzuidhof/tygo), a Go tool dependency
configured in `tygo.yaml`. They're committed, and CI fails when they're
stale: after changing a response struct, or adding one to `tygo.yaml`, run
`make types` and commit the result. Each Go package has one entry and one
output file: tygo silently ignores a second entry for the same package, so
add a new file to the package's `include_files` instead.

### Frontend dependencies

CI and the release install with `npm ci --ignore-scripts`, so no package
runs code at install. Dependabot opens one grouped npm update a week. A
pull request that adds a runtime dependency (one in `dependencies`, which
ends up in the bundle) must say why it's needed and why the platform or an
existing dependency won't do. Dev tooling in `devDependencies` is exempt.

### Test the installer

`deploy/installtest` runs `deploy/install.sh` end to end in privileged
systemd containers, one fresh container per path, against two fake
releases built from your working tree (`v0.0.1` and `v0.0.2`, for the
upgrade and downgrade paths). It needs Docker and network access to the
distro and Docker apt repositories, and takes a minute or two per distro.

```sh
make shellcheck                                 # lint the script (needs shellcheck)
make installtest                                # all four distros, on this machine's arch
INSTALLTEST_DISTROS=debian-12 make installtest  # one distro
```

The VM tier runs the paths containers can't (a real reboot, a global
IPv6 address with ufw's `IPV6=no`, snap Docker on Ubuntu 24.04,
`ssh.socket`, the upgrade from the previous published release) on KVM
VMs booted from each distro's official x64 cloud image.
`.github/workflows/installer-vm.yml` runs it nightly, by hand, and as the
release gate. Locally it needs x64 Linux with `/dev/kvm`, libvirt's
default network, `virt-install`, `qemu-img` and `cloud-localds`; without
them every path skips:

```sh
INSTALLTEST_VM_DIR=/var/lib/libvirt/images/pantalan-installtest make installtest-vm
```

### Release

Push a `vX.Y.Z` tag. `.github/workflows/release.yml` refuses any other tag
(no pre-releases), runs the same checks as a PR, builds the assets with
`deploy/release.sh`, runs the installer's VM tier on exactly those assets,
attests the binaries' provenance, and creates a draft release, uploads
every asset and publishes it last, with generated notes. If the VM tier
fails, no draft is created.
To build the same assets locally, from a clean checkout of the tag:

```sh
deploy/release.sh build v1.2.3 /tmp/pantalan-v1.2.3
```

## Try it locally

The Dashboard port speaks HTTPS with a self-signed certificate (see
[Dashboard certificate](administration.md#dashboard-certificate)), so these local examples
pass `-k`. `TOKEN=dev` is `make run`'s token; with the Docker dev stack,
use the Admin Token it prints (also in `/data/dev-secrets.env`).

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
curl -sk $API/apps/<app-id>/deployments/<deployment-id>/log -H "Authorization: Bearer $TOKEN"
curl -skN $API/apps/<app-id>/deployments/<deployment-id>/log/stream -H "Authorization: Bearer $TOKEN"

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

## Layout

```
cmd/pantalan/            entrypoint + wiring
compose.yaml, dev/        the Docker dev stack (docker compose up)
deploy/install.sh       the installer (a release asset)
deploy/release.sh       builds the release assets (used by release.yml)
deploy/installtest/     installer harness (build tag installtest)
internal/domain/        entities and rules
internal/service/       use cases + port interfaces
internal/httpapi/       REST API, webhooks, middleware
internal/dashtls/       Dashboard certificate + HTTPS listener
internal/dashboard/     serves the embedded SPA: page gate, assets, headers
internal/store/sqlite/  SQLite repositories + migrations
internal/source/        git
internal/build/         dockerfile, nixpacks
internal/runtime/docker container lifecycle
internal/health/        readiness probe
internal/proxy/         reverse proxy
internal/queue/         deploy job queue
web/                    embeds web/dist into the binary (web/embed.go)
web/app/                the Dashboard SPA (React, Vite, TypeScript)
web/app/e2e/            Playwright smoke tests against the built binary (make smoke)
web/dist/               its build (make web), gitignored but for a placeholder
tygo.yaml               API types generated into web/app/src/api/generated (make types)
docs/                   architecture notes
```
