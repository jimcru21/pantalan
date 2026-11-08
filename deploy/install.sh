#!/bin/sh
# Pantalan installer: installs, repairs or upgrades Pantalan on this Node.
#
#   curl -fsSL https://github.com/jimcru21/pantalan/releases/latest/download/install.sh | sudo sh
#   curl -fsSL .../install.sh | sudo sh -s -- --admin-user alice --dashboard-port 8443
#
# Running it is the consent: there is no "continue?" prompt. Every check and
# download happens before the first change to the host, and every step
# converges, so re-running after a failure resumes.
#
# Layout of this file: settings, then small helpers, then one function per
# step in the order main runs them. All logic is in functions and `main "$@"`
# is the last line, so a download cut off part-way runs nothing.
set -eu

# The release this script installs by default. release.yml replaces the
# placeholder with the release's tag; an unreplaced copy needs --version.
DEFAULT_VERSION=@VERSION@

REPO_URL=https://github.com/jimcru21/pantalan
INSTALL_URL=$REPO_URL/releases/latest/download/install.sh
# Test-only: where releases are downloaded from (<base>/<tag>/<asset>). The
# installer harness points it at a local fake release.
RELEASE_BASE_URL=${PANTALAN_RELEASE_BASE_URL:-$REPO_URL/releases/download}

BIN=/usr/local/bin/pantalan
CONFIG_DIR=/etc/pantalan
ENV_FILE=$CONFIG_DIR/pantalan.env
DATA_DIR=/var/lib/pantalan
UNIT_FILE=/etc/systemd/system/pantalan.service
LOG_FILE=/var/log/pantalan-install.log
SERVICE_USER=pantalan
DEFAULT_DASHBOARD_PORT=8443
MIN_DOCKER_MAJOR=28

# Flags, as given.
OPT_ADMIN_USER=
OPT_DASHBOARD_PORT=
OPT_VERSION=

# What the steps found out or decided.
STEP=checks
TMP_DIR=
TEE_PID=
VERSION=
ARCH=
OS_ID=
OS_CODENAME=
OS_NAME=
DOCKER_STATE=        # missing: install it; ok: an acceptable docker-ce is running
DASHBOARD_PORT=
NEW_BIN=             # the downloaded, verified binary in TMP_DIR
ADMIN_USER=          # empty when an Admin already exists
ADMIN_CREATED=
ADMIN_PASSWORD=
FINGERPRINT=

# ---------------------------------------------------------------------------
# Helpers

say() { printf '%s\n' "$*"; }

warn() { printf 'pantalan-install: warning: %s\n' "$*" >&2; }

die() {
	printf 'pantalan-install: %s\n' "$*" >&2
	exit 1
}

# step names what main is doing, for the failure message.
step() {
	STEP=$1
	say "==> $1"
}

usage() {
	cat <<EOF
Install, repair or upgrade Pantalan on this server.

  curl -fsSL $INSTALL_URL | sudo sh -s -- [options]

Options:
  --admin-user NAME     the Admin's username (asked for when there's a terminal)
  --dashboard-port N    the Dashboard's HTTPS port (default $DEFAULT_DASHBOARD_PORT)
  --version vX.Y.Z      install this release instead of $DEFAULT_VERSION
  -h, --help            show this help
EOF
}

# have_tty: is there a terminal to prompt on?
have_tty() { (exec 0</dev/tty) 2>/dev/null; }

# ask PROMPT: prints the line typed at the terminal.
ask() {
	printf '%s' "$1" >/dev/tty
	IFS= read -r ask_answer </dev/tty || ask_answer=
	printf '%s\n' "$ask_answer"
}

pkg_installed() {
	dpkg-query -W -f='${db:Status-Abbrev}' "$1" 2>/dev/null | grep -q '^ii'
}

is_release_tag() {
	printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'
}

# shell_words ARG...: the arguments quoted for pasting into a shell.
shell_words() {
	for sw_arg in "$@"; do
		case $sw_arg in
		*[!A-Za-z0-9._/:=@+-]* | '') printf " '%s'" "$(printf '%s' "$sw_arg" | sed "s/'/'\\\\''/g")" ;;
		*) printf ' %s' "$sw_arg" ;;
		esac
	done
}

# os_field KEY: a value from /etc/os-release, unquoted.
os_field() {
	sed -n "s/^$1=//p" /etc/os-release | sed 's/^"\(.*\)"$/\1/' | head -n 1
}

# port_holder PORT: the name of the process listening on TCP PORT, if any.
port_holder() {
	ph_lines=$(ss -Hltnp "sport = :$1" 2>/dev/null) || ph_lines=
	[ -n "$ph_lines" ] || return 0
	ph_name=$(printf '%s\n' "$ph_lines" | sed -n 's/.*users:(("\([^"]*\)".*/\1/p' | head -n 1)
	printf '%s\n' "${ph_name:-an unknown process}"
}

docker_responds() { docker version --format '{{.Server.Version}}' >/dev/null 2>&1; }

# quietly CMD...: runs CMD with its (long) output going to the log only. When
# it fails, its last lines are shown on the terminal too.
quietly() {
	q_code=0
	"$@" >"$TMP_DIR/quietly.out" 2>&1 || q_code=$?
	cat "$TMP_DIR/quietly.out" >>"$LOG_FILE"
	if [ "$q_code" -ne 0 ]; then
		tail -n 25 "$TMP_DIR/quietly.out" >&4
		die "$* failed (exit $q_code)"
	fi
}

# ---------------------------------------------------------------------------
# Output: everything goes to the terminal and to LOG_FILE, except the Admin
# password, which is printed to the terminal only.

start_log() {
	TMP_DIR=$(mktemp -d /tmp/pantalan-install.XXXXXX)
	(umask 077 && : >>"$LOG_FILE")
	chown root:root "$LOG_FILE"
	chmod 0600 "$LOG_FILE"
	mkfifo "$TMP_DIR/log"
	tee -a "$LOG_FILE" <"$TMP_DIR/log" &
	TEE_PID=$!
	# fd 3 and 4 keep the terminal for stop_log.
	exec 3>&1 4>&2 >"$TMP_DIR/log" 2>&1
	say "=== pantalan install $(date -u '+%Y-%m-%dT%H:%M:%SZ'): install.sh$(shell_words "$@")"
}

stop_log() {
	[ -n "$TEE_PID" ] || return 0
	exec 1>&3 2>&4
	wait "$TEE_PID" || :
	TEE_PID=
}

on_exit() {
	oe_code=$?
	if [ "$oe_code" -ne 0 ]; then
		printf '\npantalan-install: failed during "%s". The full log is %s.\n' "$STEP" "$LOG_FILE" >&2
		printf 'Fix the problem above and run the same command again: it picks up where it stopped.\n' >&2
	fi
	stop_log
	[ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
	exit "$oe_code"
}

# ---------------------------------------------------------------------------
# Arguments and root

parse_args() {
	while [ $# -gt 0 ]; do
		case $1 in
		--admin-user | --dashboard-port | --version)
			[ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"
			pa_value=$2
			pa_flag=$1
			shift 2
			;;
		--admin-user=* | --dashboard-port=* | --version=*)
			pa_value=${1#*=}
			pa_flag=${1%%=*}
			[ -n "$pa_value" ] || die "$pa_flag needs a value"
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown option $1 (see --help)" ;;
		esac
		case $pa_flag in
		--admin-user) OPT_ADMIN_USER=$pa_value ;;
		--dashboard-port) OPT_DASHBOARD_PORT=$pa_value ;;
		--version) OPT_VERSION=$pa_value ;;
		esac
	done
}

require_root() {
	[ "$(id -u)" -eq 0 ] && return 0
	{
		say "pantalan-install: this installer must run as root. Nothing was changed."
		say "Run it with sudo:"
		say ""
		if [ $# -eq 0 ]; then
			say "  curl -fsSL $INSTALL_URL | sudo sh"
		else
			say "  curl -fsSL $INSTALL_URL | sudo sh -s --$(shell_words "$@")"
		fi
	} >&2
	exit 1
}

# ---------------------------------------------------------------------------
# Step 1: preflight. Reads the host and changes nothing.

preflight() {
	check_os
	check_arch
	check_systemd
	check_tools
	resolve_version
	resolve_dashboard_port
	classify_docker
	check_port 80 "the app proxy needs it"
	check_port 443 "Pantalan keeps it for app HTTPS"
	check_port "$DASHBOARD_PORT" "the Dashboard needs it; choose another with --dashboard-port"
}

check_os() {
	[ -r /etc/os-release ] || die "can't read /etc/os-release, so this isn't a supported distro (Debian 12 or 13, Ubuntu 24.04 or 26.04 LTS)"
	OS_ID=$(os_field ID)
	OS_NAME=$(os_field PRETTY_NAME)
	OS_CODENAME=$(os_field UBUNTU_CODENAME)
	[ -n "$OS_CODENAME" ] || OS_CODENAME=$(os_field VERSION_CODENAME)
	case "$OS_ID $(os_field VERSION_ID)" in
	"debian 12" | "debian 13" | "ubuntu 24.04" | "ubuntu 26.04") ;;
	*) die "${OS_NAME:-this distro} isn't supported. Pantalan supports Debian 12, Debian 13, Ubuntu 24.04 LTS and Ubuntu 26.04 LTS." ;;
	esac
}

check_arch() {
	ARCH=$(dpkg --print-architecture)
	case $ARCH in
	amd64 | arm64) ;;
	*) die "the $ARCH architecture isn't supported. Pantalan supports amd64 and arm64." ;;
	esac
}

check_systemd() {
	[ -d /run/systemd/system ] || die "systemd isn't running as PID 1. Pantalan runs as a systemd service."
}

check_tools() {
	for ct_tool in curl sha256sum ss; do
		command -v "$ct_tool" >/dev/null 2>&1 || die "$ct_tool is missing; install it (apt-get install curl coreutils iproute2) and run this again"
	done
}

resolve_version() {
	VERSION=${OPT_VERSION:-$DEFAULT_VERSION}
	is_release_tag "$VERSION" && return 0
	if [ -z "$OPT_VERSION" ]; then
		die "this copy of install.sh has no release version built in; pass --version vX.Y.Z (see $REPO_URL/releases)"
	fi
	die "--version must be a release tag like v1.2.3, not $OPT_VERSION"
}

resolve_dashboard_port() {
	DASHBOARD_PORT=${OPT_DASHBOARD_PORT:-$DEFAULT_DASHBOARD_PORT}
	case $DASHBOARD_PORT in
	'' | *[!0-9]*) die "--dashboard-port must be a port number, not $DASHBOARD_PORT" ;;
	esac
	[ "$DASHBOARD_PORT" -ge 1 ] && [ "$DASHBOARD_PORT" -le 65535 ] || die "--dashboard-port must be between 1 and 65535, not $DASHBOARD_PORT"
}

# classify_docker: no Docker at all is installed later; Docker CE 28 or
# newer from Docker's repo with a daemon that answers is used as it is;
# anything else is the operator's to sort out, and nothing is removed.
classify_docker() {
	if pkg_installed docker-ce; then
		cd_version=$(dpkg-query -W -f='${Version}' docker-ce)
		cd_major=${cd_version#*:}
		cd_major=${cd_major%%.*}
		case $cd_major in
		'' | *[!0-9]*) die "can't tell which docker-ce $cd_version is" ;;
		esac
		if [ "$cd_major" -lt "$MIN_DOCKER_MAJOR" ]; then
			die "docker-ce $cd_version is installed, and Pantalan needs $MIN_DOCKER_MAJOR or newer. Pantalan never upgrades an existing Docker: upgrade it yourself (https://docs.docker.com/engine/install/$OS_ID/), then run this again."
		fi
		docker_responds || die "docker-ce $cd_version is installed but the Docker daemon doesn't answer. Check \`systemctl status docker\`, then run this again."
		DOCKER_STATE=ok
		say "Docker: docker-ce $cd_version is installed and running; using it as it is."
		return 0
	fi
	for cd_pkg in docker.io podman-docker docker-engine; do
		if pkg_installed "$cd_pkg"; then
			die "$cd_pkg is installed. Pantalan needs Docker CE from Docker's apt repository ($MIN_DOCKER_MAJOR or newer) and never removes a Docker for you. Migrate off $cd_pkg (https://docs.docker.com/engine/install/$OS_ID/), then run this again."
		fi
	done
	if [ -e /snap/bin/docker ] || docker_responds; then
		die "a Docker that isn't Docker CE from Docker's apt repository is installed (snap, or installed by hand). Pantalan never removes a Docker for you. Replace it with Docker CE $MIN_DOCKER_MAJOR or newer (https://docs.docker.com/engine/install/$OS_ID/), then run this again."
	fi
	DOCKER_STATE=missing
	say "Docker: not installed; Docker CE will be installed from Docker's apt repository."
}

# check_port PORT WHY: PORT must be free, or held by Pantalan itself.
check_port() {
	cp_holder=$(port_holder "$1")
	case $cp_holder in
	'' | pantalan) ;;
	*) die "port $1 is in use by $cp_holder, and $2" ;;
	esac
}

# ---------------------------------------------------------------------------
# Step 2: download and verify the release binary into TMP_DIR.

download_release() {
	dr_asset=pantalan_${VERSION}_linux_${ARCH}
	say "Downloading Pantalan $VERSION for linux/$ARCH from $RELEASE_BASE_URL/$VERSION/"
	fetch "$RELEASE_BASE_URL/$VERSION/$dr_asset" "$TMP_DIR/$dr_asset"
	fetch "$RELEASE_BASE_URL/$VERSION/SHA256SUMS" "$TMP_DIR/SHA256SUMS"
	# --ignore-missing skips the other arch's binary, so check that ours was
	# listed at all.
	awk -v f="$dr_asset" '$2 == f || $2 == "*" f { found = 1 } END { exit !found }' "$TMP_DIR/SHA256SUMS" ||
		die "SHA256SUMS for $VERSION doesn't list $dr_asset. Nothing was installed."
	(cd "$TMP_DIR" && sha256sum -c --ignore-missing --quiet SHA256SUMS) ||
		die "checksum mismatch for $dr_asset: the download is corrupt or was tampered with. Nothing was installed."
	chmod 0755 "$TMP_DIR/$dr_asset"
	NEW_BIN=$TMP_DIR/$dr_asset
	dr_reports=$("$NEW_BIN" version) || die "the downloaded $dr_asset doesn't run on this host"
	[ "$dr_reports" = "$VERSION" ] || die "the downloaded $dr_asset says it is $dr_reports, not $VERSION"
	say "Verified $dr_asset against SHA256SUMS."
}

fetch() {
	curl -fsSL --retry 3 --connect-timeout 20 -o "$2" "$1" || die "couldn't download $1"
}

# ---------------------------------------------------------------------------
# Step 3: collect what's missing, and check it with the downloaded binary.

collect_inputs() {
	if admin_exists; then
		say "An Admin already exists; keeping it."
		return 0
	fi
	ADMIN_USER=$OPT_ADMIN_USER
	if [ -z "$ADMIN_USER" ]; then
		have_tty || die "--admin-user is required: there's no terminal to ask on. Pass it, e.g. curl -fsSL $INSTALL_URL | sudo sh -s -- --admin-user alice"
		ADMIN_USER=$(ask "Admin username: ")
	fi
	while ! ci_problem=$("$NEW_BIN" admin create --username "$ADMIN_USER" --check 2>&1); do
		ci_problem=${ci_problem#pantalan: }
		have_tty || die "--admin-user $ADMIN_USER: $ci_problem"
		say "$ci_problem" >/dev/tty
		ADMIN_USER=$(ask "Admin username: ")
	done
}

# admin_exists: is there an Admin in the database already? Asks the
# installed binary, which knows the schema the database is at.
admin_exists() {
	[ -f "$DATA_DIR/pantalan.db" ] && [ -x "$BIN" ] || return 1
	ae_code=0
	"$BIN" admin show >/dev/null 2>"$TMP_DIR/admin-show.err" || ae_code=$?
	case $ae_code in
	0) return 0 ;;
	3) return 1 ;;
	*)
		cat "$TMP_DIR/admin-show.err" >&2
		die "couldn't check whether an Admin exists"
		;;
	esac
}

# ---------------------------------------------------------------------------
# Step 4: packages. The first change to the host.

install_packages() {
	ip_missing=
	for ip_pkg in git ca-certificates; do
		pkg_installed "$ip_pkg" || ip_missing="$ip_missing $ip_pkg"
	done
	if [ -n "$ip_missing" ]; then
		say "Installing$ip_missing"
		quietly apt-get update
		# shellcheck disable=SC2086 # one word per package
		quietly apt_install $ip_missing
	fi
	[ "$DOCKER_STATE" = ok ] || install_docker
	quietly systemctl enable --now docker.service
	wait_for_docker
}

apt_install() {
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends "$@"
}

# install_docker follows https://docs.docker.com/engine/install/debian/ (and
# /ubuntu/): Docker's key and deb822 source, then its packages.
install_docker() {
	say "Installing Docker CE from Docker's apt repository"
	install -m 0755 -d /etc/apt/keyrings
	fetch "https://download.docker.com/linux/$OS_ID/gpg" /etc/apt/keyrings/docker.asc
	chmod a+r /etc/apt/keyrings/docker.asc
	cat >/etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/$OS_ID
Suites: $OS_CODENAME
Components: stable
Architectures: $ARCH
Signed-By: /etc/apt/keyrings/docker.asc
EOF
	# Only a Docker installed here gets live-restore, so app containers keep
	# running through Docker upgrades. An existing daemon.json is the
	# operator's.
	if [ ! -e /etc/docker/daemon.json ]; then
		install -m 0755 -d /etc/docker
		printf '{\n  "live-restore": true\n}\n' >/etc/docker/daemon.json
	fi
	quietly apt-get update
	quietly apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
}

wait_for_docker() {
	wd_i=0
	until docker_responds; do
		wd_i=$((wd_i + 1))
		[ "$wd_i" -lt 30 ] || die "Docker doesn't answer. Check \`systemctl status docker\`."
		sleep 1
	done
	say "Docker $(docker version --format '{{.Server.Version}}') is running."
}

# ---------------------------------------------------------------------------
# Step 5: the service user, directories, env file and binary.

setup_files() {
	if ! getent passwd "$SERVICE_USER" >/dev/null; then
		useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home \
			--shell /usr/sbin/nologin --comment "Pantalan service" "$SERVICE_USER"
	fi
	# The docker group is effectively root: the unit's hardening is defence
	# in depth, not a boundary.
	usermod --append --groups docker "$SERVICE_USER"
	install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
	install -d -m 0750 -o root -g "$SERVICE_USER" "$CONFIG_DIR"
	write_env_file
	chown "root:$SERVICE_USER" "$ENV_FILE"
	chmod 0640 "$ENV_FILE"
	# Replaced atomically: a rename within /usr/local/bin.
	install -m 0755 -o root -g root "$NEW_BIN" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
}

# write_env_file writes the service's settings once, with a new Admin Token
# that is never printed.
write_env_file() {
	[ ! -e "$ENV_FILE" ] || return 0
	we_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
	[ ${#we_token} -eq 64 ] || die "couldn't generate an Admin Token"
	(
		umask 027
		cat >"$ENV_FILE.new" <<EOF
# Pantalan's settings, read by pantalan.service. After editing:
#   sudo systemctl restart pantalan
PANTALAN_ADMIN_TOKEN=$we_token
PANTALAN_DASHBOARD_ADDR=:$DASHBOARD_PORT
EOF
	)
	chown "root:$SERVICE_USER" "$ENV_FILE.new"
	mv -f "$ENV_FILE.new" "$ENV_FILE"
	say "Wrote $ENV_FILE with a new Admin Token."
}

# ---------------------------------------------------------------------------
# Step 6: the Admin, when there is none.

ensure_admin() {
	ea_code=0
	"$BIN" admin show >/dev/null 2>"$TMP_DIR/admin.err" || ea_code=$?
	case $ea_code in
	0) return 0 ;;
	3) ;;
	*)
		cat "$TMP_DIR/admin.err" >&2
		die "couldn't check whether an Admin exists"
		;;
	esac
	# stdout is the password alone; it stays out of the log.
	ADMIN_PASSWORD=$("$BIN" admin create --username "$ADMIN_USER" 2>"$TMP_DIR/admin.err") || {
		cat "$TMP_DIR/admin.err" >&2
		die "couldn't create the Admin $ADMIN_USER"
	}
	ADMIN_USER=$("$BIN" admin show)
	ADMIN_CREATED=1
	say "Created the Admin $ADMIN_USER."
}

# ---------------------------------------------------------------------------
# Step 7: the unit, and the service answering on HTTPS.

start_service() {
	write_unit
	systemctl daemon-reload
	systemctl enable --quiet pantalan.service
	systemctl restart pantalan.service
	wait_for_dashboard
	FINGERPRINT=$("$BIN" tls fingerprint)
}

# write_unit overwrites the unit on every run; local changes belong in
# pantalan.service.d/ drop-ins.
write_unit() {
	cat >"$UNIT_FILE.new" <<EOF
# Written by the Pantalan installer and replaced on every run. Put your own
# changes in /etc/systemd/system/pantalan.service.d/*.conf.
[Unit]
Description=Pantalan
Documentation=$REPO_URL
Wants=network-online.target docker.service
After=network-online.target docker.service

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
EnvironmentFile=$ENV_FILE
ExecStart=$BIN serve
Restart=on-failure
RestartSec=2s
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=$DATA_DIR
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF
	chmod 0644 "$UNIT_FILE.new"
	mv -f "$UNIT_FILE.new" "$UNIT_FILE"
}

wait_for_dashboard() {
	wd_deadline=$(($(date +%s) + 30))
	until curl -ksf -o /dev/null --max-time 2 "https://127.0.0.1:$DASHBOARD_PORT/healthz"; do
		if [ "$(date +%s)" -ge "$wd_deadline" ]; then
			journalctl -u pantalan.service -n 50 --no-pager >&2 || :
			die "the service didn't answer on https://127.0.0.1:$DASHBOARD_PORT within 30 s; its last 50 log lines are above"
		fi
		sleep 1
	done
	say "The service answers on https://127.0.0.1:$DASHBOARD_PORT."
}

# ---------------------------------------------------------------------------
# Step 9: the summary.

# default_ip: the IPv4 address of the interface the default route uses.
default_ip() {
	ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1
}

is_private_ip() {
	case $1 in
	10.* | 192.168.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) return 0 ;;
	100.6[4-9].* | 100.[7-9][0-9].* | 100.1[01][0-9].* | 100.12[0-7].*) return 0 ;; # 100.64/10
	esac
	return 1
}

# print_summary PASSWORD: PASSWORD stands in for the Admin password, so the
# same text serves the terminal and the log.
print_summary() {
	ps_ip=$(default_ip)
	say ""
	say "Pantalan $VERSION is running on this Node."
	say ""
	say "  Dashboard       https://${ps_ip:-<this server>}:$DASHBOARD_PORT"
	if [ -n "$ps_ip" ] && is_private_ip "$ps_ip"; then
		say "                  Behind NAT? $ps_ip is a private address: use this server's public IP instead."
	fi
	say "  Fingerprint     $FINGERPRINT  (compare this with what your browser shows when it warns)"
	if [ -n "$ADMIN_CREATED" ]; then
		say "  Admin           $ADMIN_USER"
		say "  Admin password$1"
	fi
	say "  Admin Token     in $ENV_FILE (PANTALAN_ADMIN_TOKEN)"
	say "  Lost password   sudo pantalan admin reset-password"
	say ""
	say "  Cloud security groups are outside this server's firewall: open 80, 443 and $DASHBOARD_PORT there too."
	say "  Install log: $LOG_FILE"
}

# ---------------------------------------------------------------------------

main() {
	# The script itself may be arriving on stdin (curl | sh); nothing below
	# reads stdin, and prompts use /dev/tty.
	exec </dev/null
	parse_args "$@"
	require_root "$@"
	start_log "$@"
	trap on_exit EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM

	step "Checking this host"
	preflight
	step "Downloading Pantalan $VERSION"
	download_release
	step "Checking the Admin username"
	collect_inputs
	step "Installing packages"
	install_packages
	step "Setting up files"
	setup_files
	step "Creating the Admin"
	ensure_admin
	step "Starting the service"
	start_service
	# The firewall goes here: last, once the service is confirmed up.

	STEP="Printing the summary"
	stop_log
	print_summary "  $ADMIN_PASSWORD  (shown only now: sign in and keep it safe)"
	print_summary ": shown on screen only" >>"$LOG_FILE"
}

main "$@"
