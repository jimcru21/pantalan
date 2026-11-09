#!/bin/sh
# The pantalan dev container's entrypoint (dev/Dockerfile): wait for the
# dind daemon, make the dev secrets and the Admin on first start, then hand
# over to air, which builds, runs and rebuilds `pantalan serve`.
set -eu

SECRETS=$PANTALAN_DATA_DIR/dev-secrets.env

say() { printf 'pantalan-dev: %s\n' "$*"; }

die() {
	printf 'pantalan-dev: %s\n' "$*" >&2
	exit 1
}

random_hex() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }

say "waiting for the Docker daemon (the docker service)"
i=0
until docker version >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -le 60 ] || die "the Docker daemon didn't answer in 60 s: docker compose logs docker"
	sleep 1
done

# The Admin Token and webhook secret are generated once and kept with the
# data, never well-known values: the stack may be reachable from the
# internet (PANTALAN_DEV_BIND), and the token can deploy anything.
mkdir -p "$PANTALAN_DATA_DIR"
if [ ! -s "$SECRETS" ]; then
	umask 077
	printf 'PANTALAN_ADMIN_TOKEN=%s\nPANTALAN_WEBHOOK_SECRET=%s\n' "$(random_hex)" "$(random_hex)" >"$SECRETS.new"
	mv "$SECRETS.new" "$SECRETS"
	umask 022
fi
set -a
# shellcheck disable=SC1090 # written just above
. "$SECRETS"
set +a

say "building pantalan"
go build -o ./tmp/pantalan ./cmd/pantalan

admin_password=
code=0
./tmp/pantalan admin show >/dev/null 2>&1 || code=$?
case $code in
0) ;;
3) admin_password=$(./tmp/pantalan admin create --username "$PANTALAN_DEV_ADMIN") ;;
*) ./tmp/pantalan admin show || die "couldn't read the Admin from $PANTALAN_DATA_DIR" ;;
esac

cat <<EOF

  Pantalan dev stack
  Dashboard     https://${PANTALAN_DEV_HOST}:${PANTALAN_DEV_WEB_PORT}  (Vite, hot reload; self-signed: accept the browser's warning)
  API           https://${PANTALAN_DEV_HOST}:${PANTALAN_DEV_DASHBOARD_PORT}  (also the embedded Dashboard, once make web has built it)
  App proxy     http://${PANTALAN_DEV_HOST}:${PANTALAN_DEV_PROXY_PORT}
  Admin         $(./tmp/pantalan admin show)
EOF
if [ -n "$admin_password" ]; then
	printf '  Password      %s  (shown once: docker compose exec pantalan pantalan admin reset-password)\n' "$admin_password"
else
	printf '  Password      as printed on first start; new one: docker compose exec pantalan pantalan admin reset-password\n'
fi
cat <<EOF
  Admin Token   $PANTALAN_ADMIN_TOKEN
  Secrets file  $SECRETS (in the data volume)

  Edit a Go file and the server rebuilds and restarts; edits under
  web/app/src show up in the Dashboard at once.

EOF

exec air -c dev/air.toml
