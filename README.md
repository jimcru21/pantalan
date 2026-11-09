<div align="center">

# Pantalan

**Your own little Heroku. Push to git, get a running app.**

A self-hosted platform that builds your apps from git and runs them as Docker
containers on a server you own. One Go binary, one install command.

[![CI](https://github.com/jimcru21/pantalan/actions/workflows/ci.yml/badge.svg)](https://github.com/jimcru21/pantalan/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/jimcru21/pantalan)](https://github.com/jimcru21/pantalan/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/jimcru21/pantalan)](go.mod)

[Quick start](#-quick-start) •
[Deploy your first app](#-deploy-your-first-app) •
[Push to deploy](#-push-to-deploy) •
[Docs](#-documentation) •
[Contributing](#-contributing)

</div>

---

## 🤔 What is Pantalan?

*Pantalan* is Filipino for **pier**: the place where boats come in and get
unloaded. Your git repos are the boats.

You give Pantalan a git repository with a `Dockerfile`. It clones it, builds
an image, starts the container, checks that it's healthy, and routes your
domain to it. Push a new commit and it does it all again, **without
downtime**: the old version keeps serving until the new one is ready.

It's in the same family as [Coolify](https://coolify.io),
[Dokploy](https://dokploy.com) and Tugport, but deliberately small: one
binary next to Docker, a SQLite file, and a systemd service. Nothing else to
run.

## ✨ Features

- 🚀 **Deploy from git.** Any public repo with a `Dockerfile` at its root.
- 🔁 **Zero-downtime deploys.** Blue-green swap: the new container must pass
  a health check before it gets traffic.
- 🪝 **Push to deploy.** A GitHub webhook redeploys every app tracking the
  pushed branch.
- 🌐 **Built-in reverse proxy.** Routes each domain to its app, no Nginx or
  Traefik needed.
- 📊 **Dashboard.** See your server's CPU, memory, disk, containers, images
  and Docker events at a glance.
- 🔌 **REST API.** Everything the Dashboard can't do yet, the API can.
- 🛡️ **Secure by default.** HTTPS Dashboard, generated passwords, throttled
  sign-ins, and a firewall that's configured for you.
- 📦 **One-command install, upgrade and repair.** The same command does all
  three.

> [!NOTE]
> Pantalan is young (v0.x). Today it manages **one server**, apps are
> created through the **API** (the Dashboard shows the server's health), and
> apps are served over **plain HTTP** on port 80. Automatic HTTPS for apps
> and app management in the Dashboard are on the way.

## 📋 Requirements

| | |
|---|---|
| **OS** | A fresh Debian 12, Debian 13, Ubuntu 24.04 LTS or Ubuntu 26.04 LTS |
| **CPU** | amd64 or arm64 |
| **Access** | A user who can `sudo` |
| **Ports** | 80, 443 and 8443 free (8443 is the Dashboard, and can be changed) |

Docker, git and the firewall are set up by the installer. If Docker CE 28 or
newer is already installed, it's used as it is.

## 🚀 Quick start

### 1. Install

SSH into your server and run:

```sh
curl -fL --max-time 120 -o install.sh https://github.com/jimcru21/pantalan/releases/latest/download/install.sh
sudo sh install.sh
```

It asks for an Admin username, then does the rest. It takes a minute or
two. When it's done you'll see a summary like this:

```text
Pantalan v0.1.0 is running on this Node.

  Dashboard       https://203.0.113.10:8443
  Fingerprint     SHA256:AB:CD:...  (compare this with what your browser shows when it warns)
  Admin           alice
  Admin password  XXXXXXXXXXXXXXXXXXXXXXXXXX  (shown only now: sign in and keep it safe)
  Admin Token     in /etc/pantalan/pantalan.env (PANTALAN_ADMIN_TOKEN)
  Lost password   sudo pantalan admin reset-password
  Firewall        ...
```

> [!IMPORTANT]
> **Save the Admin password now.** It's shown only once. Lost it? Run
> `sudo pantalan admin reset-password` for a new one.

### 2. Open the Dashboard

Go to `https://<your-server-ip>:8443`.

Your browser will warn you about the certificate. That's expected: the
Dashboard uses a self-signed certificate. Check that the fingerprint your
browser shows matches the one in the summary, then continue.

### 3. Sign in

Use the Admin username and the password from the summary. You'll land on
the Node overview, showing your server's health.

🎉 **That's it, Pantalan is running.**

> [!TIP]
> Using a cloud provider (AWS, Hetzner, DigitalOcean, ...)? Its firewall or
> security group sits in front of your server: open ports **80**, **443**
> and **8443** there too.

## 📦 Deploy your first app

Apps are created through the API for now. You'll need:

- a **git repo** with a `Dockerfile` at its root,
- the **port** your app listens on inside its container,
- a **domain** whose DNS `A` record points at your server.

**1. Get your Admin Token** (on the server):

```sh
sudo grep PANTALAN_ADMIN_TOKEN /etc/pantalan/pantalan.env
```

**2. Set two shortcuts** (on the server, or anywhere that can reach it):

```sh
TOKEN=<your-admin-token>
API=https://<your-server-ip>:8443/api/v1
```

**3. Create the app:**

```sh
curl -sk $API/apps \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "name": "hello",
    "repo_url": "https://github.com/<you>/<repo>.git",
    "branch": "main",
    "domain": "hello.example.com",
    "port": 8080
  }'
```

The response includes the app's `id`. Optional fields: `branch` (defaults to
`main`) and `env`, an object of environment variables such as
`{"DATABASE_URL": "..."}`.

**4. Deploy it:**

```sh
curl -sk -X POST $API/apps/<app-id>/deployments -H "Authorization: Bearer $TOKEN"
```

To deploy a specific branch, tag or commit, send `{"ref": "<branch|tag|sha>"}`
as the body.

**5. Watch it go live:**

```sh
curl -sk $API/apps/<app-id>/deployments -H "Authorization: Bearer $TOKEN"     # status
curl -sk $API/deployments/<deployment-id>/logs -H "Authorization: Bearer $TOKEN"  # Deployment Log
```

A Deployment goes `queued` → `building` → `starting` → `live`. If the new
version fails its health check, it's marked `failed` and the old one keeps
serving.

**6. Open it:** visit `http://hello.example.com`. 🎉

> [!NOTE]
> `-k` tells curl to accept the Dashboard's self-signed certificate.

<details>
<summary><b>All API endpoints</b></summary>

All under `/api/v1`, with `Authorization: Bearer <Admin Token>`.

| Method | Path | What it does |
|---|---|---|
| `GET` | `/apps` | List apps |
| `POST` | `/apps` | Create an app |
| `GET` | `/apps/{id}` | Get one app |
| `GET` | `/apps/{id}/deployments` | List an app's Deployments |
| `POST` | `/apps/{id}/deployments` | Deploy (body optional: `{"ref": "..."}`) |
| `GET` | `/deployments/{id}/logs` | A Deployment's log |

`GET /healthz` needs no credentials and answers `200` when the service is up.

</details>

## 🪝 Push to deploy

Make every `git push` redeploy your app automatically.

**1. Pick a webhook secret** and add it to the server's settings:

```sh
echo "PANTALAN_WEBHOOK_SECRET=$(openssl rand -hex 32)" | sudo tee -a /etc/pantalan/pantalan.env
sudo systemctl restart pantalan
sudo grep PANTALAN_WEBHOOK_SECRET /etc/pantalan/pantalan.env   # copy this value
```

**2. Add the webhook on GitHub**, in your repo's **Settings → Webhooks →
Add webhook**:

| Field | Value |
|---|---|
| Payload URL | `https://<your-server-ip>:8443/webhooks/github` |
| Content type | `application/json` |
| Secret | the value of `PANTALAN_WEBHOOK_SECRET` |
| SSL verification | **Disable** (the Dashboard's certificate is self-signed) |
| Events | Just the `push` event |

Now pushing to the app's branch starts a Deployment of that commit. The
app's `repo_url` must be the repo's `https://github.com/....git` clone URL
for the push to find it.

> [!NOTE]
> Disabling SSL verification only stops GitHub checking *who* it's talking
> to. The traffic is still encrypted, and Pantalan rejects any payload
> whose signature doesn't match your secret.

## 🧰 Everyday commands

Run these on the server.

| Task | Command |
|---|---|
| Upgrade to the latest release | `sudo sh install.sh` (download it again first) |
| Repair a broken install | `sudo sh install.sh` |
| New Admin password | `sudo pantalan admin reset-password` |
| Show the Admin's username | `sudo pantalan admin show` |
| Check the Dashboard certificate | `sudo pantalan tls fingerprint` |
| Installed version | `pantalan version` |
| Service status | `sudo systemctl status pantalan` |
| Service logs | `sudo journalctl -u pantalan -f` |
| Restart after editing settings | `sudo systemctl restart pantalan` |
| Uninstall (keeps your data) | `sudo sh install.sh --uninstall` |
| Remove everything | `sudo sh install.sh --purge` |

Re-running the installer is always safe: it checks what's there and only
does what's missing. See the [installation guide](docs/installation.md) for
upgrades, rollbacks and what uninstall keeps.

## 🔧 Configuration

Settings live in `/etc/pantalan/pantalan.env`. After changing one, run
`sudo systemctl restart pantalan`.

| Variable | Default | What it does |
|---|---|---|
| `PANTALAN_ADMIN_TOKEN` | *(generated)* | Bearer token for the API |
| `PANTALAN_DASHBOARD_ADDR` | `:8443` | Dashboard, API, webhooks (HTTPS) |
| `PANTALAN_PROXY_ADDR` | `:80` | Where apps are served |
| `PANTALAN_DATA_DIR` | `/var/lib/pantalan` | Database, builds, logs, certificate |
| `PANTALAN_WEBHOOK_SECRET` | *(empty: webhooks off)* | GitHub webhook secret |
| `PANTALAN_WORKERS` | `2` | How many Deployments run at once |
| `PANTALAN_FIREWALL` | `ufw` | Installer only: `off` leaves the firewall alone |

> [!TIP]
> To move the Dashboard to another port, run the installer with
> `--dashboard-port N` instead of editing the file, so the firewall rule
> moves too.

## 📚 Documentation

| Guide | For |
|---|---|
| [Installation](docs/installation.md) | Install flags, what the installer changes, firewall, upgrade, rollback, uninstall, verifying releases |
| [Administration](docs/administration.md) | The Admin account, sign-in rules, the Dashboard certificate, all settings |
| [Development](docs/development.md) | Running Pantalan locally, testing the installer, releasing, code layout |
| [Architecture](docs/architecture.md) | How Pantalan is built and how a Deployment works |
| [Glossary](GLOSSARY.md) | What we mean by Node, App, Deployment, Admin, ... |
| [Decisions](docs/adr) | Why things are the way they are |

## 🔒 Security

- The Dashboard is **HTTPS only**, with a self-signed certificate. Always
  compare its fingerprint (`sudo pantalan tls fingerprint`) with what your
  browser shows before signing in.
- The **Admin password** is always generated, never chosen. Repeated failed
  sign-ins are slowed down, up to 15 minutes between attempts.
- The **Admin Token** works only for the API, never for the Dashboard. Keep
  it secret: anyone with it can deploy containers on your server.
- App containers listen only on `127.0.0.1`, reachable through the proxy.
- Anything in the `docker` group is effectively root on the server. Only
  run code you trust.

More in [Administration](docs/administration.md).

## 🤝 Contributing

The quickest way to hack on Pantalan only needs Docker:

```sh
git clone https://github.com/jimcru21/pantalan.git
cd pantalan
docker compose up
```

It prints the dev Admin's password and Admin Token, serves the Dashboard on
https://localhost:8443, and rebuilds within seconds whenever you save a
file. Apps you deploy are served at http://localhost:8080.

Before opening a pull request:

```sh
make check   # go vet, race tests and gofmt
```

Commit titles follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat(proxy): route requests by Host header`). See
[Development](docs/development.md) for everything else, and
[open an issue](https://github.com/jimcru21/pantalan/issues) for bugs and
ideas.

## 🙏 Inspiration

Pantalan stands on the shoulders of [Coolify](https://coolify.io),
[Dokploy](https://dokploy.com) and Tugport, which showed how pleasant
self-hosting can be.
