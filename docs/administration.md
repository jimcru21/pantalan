# Administration

The Admin account, the Dashboard's certificate, and every setting the
service reads.

## Admin account

The Admin is the one account that signs in to the Dashboard. Its password is
always generated (26 base32 characters) and printed once; you can't choose
one. Run these on the server:

```sh
sudo pantalan admin create --username alice   # prints the password; refuses if an Admin exists
sudo pantalan admin show                      # prints the username; exits 3 when there is no Admin; only reads
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
`/api/v1` only and is refused by the Dashboard's pages. `/api/v1` also
accepts a Dashboard session cookie, but only from the Dashboard's own
origin; token requests work from anywhere.
`admin create --username NAME --check` only validates the name and never
touches the database. Usernames are 1 to 32 characters of `a-z`, `0-9`, `.`,
`_` and `-`, starting with a letter, and are lowercased. The Admin can't be
renamed.

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
| `PANTALAN_FIREWALL` | `ufw` | Installer only, ignored by the service: `off` leaves the firewall alone (see [Firewall](installation.md#firewall)) |
