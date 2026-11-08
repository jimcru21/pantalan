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
DROPIN_DIR=$UNIT_FILE.d
LOG_FILE=/var/log/pantalan-install.log
SERVICE_USER=pantalan
DEFAULT_DASHBOARD_PORT=8443
MIN_DOCKER_MAJOR=28

# Flags, as given.
OPT_ADMIN_USER=
OPT_DASHBOARD_PORT=
OPT_VERSION=
OPT_UNINSTALL=
OPT_PURGE=
OPT_YES=
OPT_NO_FIREWALL=

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
FIREWALL_MODE=       # ufw or off, as stored in ENV_FILE (PANTALAN_FIREWALL)
FW_ACTION=           # enable, add (ufw already active), stage, skip or off
SSH_PORTS=           # the SSH ports found, space-separated
FW_LINE=             # the summary's Firewall line
FW_WARNINGS=         # lines the summary repeats under it
FW_REASON=           # why ufw is staged rather than enabled, or skipped

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
  --no-firewall         leave the firewall alone, now and on every re-run
  --uninstall           remove the service and binary; keep data, config and apps
  --purge               remove everything --uninstall keeps too (asks you to type purge)
  --yes                 with --purge and no terminal: purge without asking
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

# env_setting KEY: KEY's value in ENV_FILE, empty when it or the file is
# missing.
env_setting() {
	[ -r "$ENV_FILE" ] || return 0
	sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1
}

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
			if [ $# -lt 2 ] || [ -z "$2" ]; then
				die "$1 needs a value"
			fi
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
		--uninstall | --purge | --yes | --no-firewall)
			pa_flag=$1
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
		--uninstall) OPT_UNINSTALL=1 ;;
		--purge) OPT_PURGE=1 ;;
		--yes) OPT_YES=1 ;;
		--no-firewall) OPT_NO_FIREWALL=1 ;;
		esac
	done
	check_flag_combinations
}

# check_flag_combinations: --yes only answers --purge's question, and the
# install flags mean nothing to a removal.
check_flag_combinations() {
	if [ -n "$OPT_YES" ] && [ -z "$OPT_PURGE" ]; then
		die "--yes is only valid with --purge (it confirms the purge when there's no terminal to type purge on)"
	fi
	[ -n "$OPT_UNINSTALL$OPT_PURGE" ] || return 0
	for cf_pair in "--admin-user=$OPT_ADMIN_USER" "--dashboard-port=$OPT_DASHBOARD_PORT" "--version=$OPT_VERSION" "--no-firewall=$OPT_NO_FIREWALL"; do
		[ -z "${cf_pair#*=}" ] || die "${cf_pair%%=*} is for installing, and can't be used with --uninstall or --purge"
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
	classify_firewall
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
	# An installed Node's port is the one its env file says; a flag that
	# disagrees stops the run instead of silently losing to it.
	rd_stored=$(env_setting PANTALAN_DASHBOARD_ADDR)
	rd_stored_port=${rd_stored##*:}
	if [ -n "$OPT_DASHBOARD_PORT" ] && [ -n "$rd_stored" ] && [ "$OPT_DASHBOARD_PORT" != "$rd_stored_port" ]; then
		die "--dashboard-port $OPT_DASHBOARD_PORT disagrees with PANTALAN_DASHBOARD_ADDR=$rd_stored in $ENV_FILE. Nothing was changed. To move the Dashboard, edit pantalan.env, then run this again without --dashboard-port."
	fi
	DASHBOARD_PORT=${OPT_DASHBOARD_PORT:-${rd_stored_port:-$DEFAULT_DASHBOARD_PORT}}
	case $DASHBOARD_PORT in
	'' | *[!0-9]*) die "--dashboard-port must be a port number, not $DASHBOARD_PORT" ;;
	esac
	if [ "$DASHBOARD_PORT" -lt 1 ] || [ "$DASHBOARD_PORT" -gt 65535 ]; then
		die "--dashboard-port must be between 1 and 65535, not $DASHBOARD_PORT"
	fi
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
# installed binary, which knows the schema the database is at. After
# --uninstall there is none, so the downloaded one asks instead: it migrates
# a kept database to its own schema (which the install would do anyway), and
# refuses one newer than it knows.
admin_exists() {
	[ -f "$DATA_DIR/pantalan.db" ] || return 1
	ae_bin=$BIN
	[ -x "$ae_bin" ] || ae_bin=$NEW_BIN
	ae_code=0
	"$ae_bin" admin show >/dev/null 2>"$TMP_DIR/admin-show.err" || ae_code=$?
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
	for ip_pkg in git ca-certificates $(firewall_package); do
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
	add_env_setting PANTALAN_FIREWALL "$FIREWALL_MODE"
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
# Installer only (the service ignores it): ufw, or off to leave the firewall
# alone. Every run of the installer converges to it.
PANTALAN_FIREWALL=$FIREWALL_MODE
EOF
	)
	chown "root:$SERVICE_USER" "$ENV_FILE.new"
	mv -f "$ENV_FILE.new" "$ENV_FILE"
	say "Wrote $ENV_FILE with a new Admin Token."
}

# add_env_setting KEY VALUE appends KEY to an env file written before KEY
# existed (by an older installer); one that has it is left as it is.
add_env_setting() {
	grep -q "^$1=" "$ENV_FILE" && return 0
	printf '%s=%s\n' "$1" "$2" >>"$ENV_FILE"
	say "Added $1=$2 to $ENV_FILE."
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
# Step 8: the firewall, last, once the service is up. Only SSH, 80, 443 and
# the Dashboard port are let in, and enabling it must never lock the
# operator out. Pantalan's rules carry the comment `pantalan` (80, 443, the
# Dashboard port: made to match that set on every run) or `pantalan-ssh`
# (only ever added, never removed). The installer never runs `ufw disable`
# or `ufw reset`, never edits /etc/default/ufw and adds no DOCKER-USER rules.
# Its read-only half (classify_firewall) runs in preflight.

# classify_firewall works out the mode and what to do with this host's
# firewall (FW_ACTION), and finds the SSH ports.
classify_firewall() {
	resolve_firewall_mode
	if [ "$FIREWALL_MODE" = off ]; then
		FW_ACTION=off
		say "Firewall: off (PANTALAN_FIREWALL=off): left alone."
		return 0
	fi
	FW_REASON=$(other_firewall_manager)
	if [ -n "$FW_REASON" ]; then
		# Two managers would fight over the same ruleset.
		FW_ACTION=skip
		warn "$FW_REASON, so Pantalan leaves ufw alone: open $(comma_list "80 443 $DASHBOARD_PORT") in it"
		return 0
	fi
	detect_ssh_ports
	if ufw_active; then
		# The operator's firewall: rules are added to it, and nothing of
		# theirs (rules, policies) is touched.
		FW_ACTION=add
		check_ufw_policy
		return 0
	fi
	FW_REASON=$(stage_reason)
	if [ -n "$FW_REASON" ]; then
		FW_ACTION=stage
		warn "ufw won't be enabled ($FW_REASON): its rules will be staged for you to enable after checking SSH"
		return 0
	fi
	FW_ACTION=enable
}

# stage_reason: why enabling ufw here could cut off SSH or break Docker,
# if it could.
stage_reason() {
	if [ "$(ufw_default MANAGE_BUILTINS)" = yes ]; then
		# Enabling would flush the built-in chains, Docker's rules with them.
		printf '%s\n' "MANAGE_BUILTINS=yes in /etc/default/ufw"
	elif [ "$(ufw_default IPV6)" = no ] && [ -n "$(ip -6 addr show scope global 2>/dev/null)" ]; then
		# An SSH session over IPv6 would drop as soon as ufw came up.
		printf '%s\n' "IPV6=no in /etc/default/ufw on a host with a global IPv6 address"
	elif docker_nftables; then
		printf '%s\n' "Docker uses its nftables firewall backend"
	elif [ -z "$SSH_PORTS" ]; then
		printf '%s\n' "no SSH server found"
	fi
}

# ufw_default KEY: KEY's value in /etc/default/ufw (left from an earlier
# ufw, or the operator's), unquoted; empty before ufw is installed.
ufw_default() {
	[ -r /etc/default/ufw ] || return 0
	sed -n "s/^$1=//p" /etc/default/ufw | tr -d '"' | tail -n 1
}

# docker_nftables: is Docker set up for its own nftables backend, which
# has no DOCKER-USER chain and isn't tested alongside ufw?
docker_nftables() {
	grep -Eq '"firewall-backend"[[:space:]]*:[[:space:]]*"nftables"' /etc/docker/daemon.json 2>/dev/null && return 0
	docker_responds || return 1
	[ "$(docker info --format '{{with .FirewallBackend}}{{.Driver}}{{end}}' 2>/dev/null)" = nftables ]
}

# other_firewall_manager: what other than ufw manages this host's firewall,
# if anything. nftables.service counts only with a ruleset of the
# operator's: Docker CE pulls in the nftables package, which on Debian and
# Ubuntu enables the service with its stock accept-everything ruleset.
other_firewall_manager() {
	if systemctl is-active --quiet firewalld.service 2>/dev/null; then
		printf '%s\n' "firewalld is active"
	elif systemctl is-enabled --quiet nftables.service 2>/dev/null && ! nftables_conf_is_stock; then
		printf '%s\n' "nftables.service is enabled with its own ruleset"
	fi
}

# nftables_conf_is_stock: is /etc/nftables.conf the one the nftables
# package shipped?
nftables_conf_is_stock() {
	ns_shipped=$(dpkg-query -W -f='${Conffiles}\n' nftables 2>/dev/null | awk '$1 == "/etc/nftables.conf" { print $2 }')
	[ -n "$ns_shipped" ] && [ "$(md5sum </etc/nftables.conf 2>/dev/null | cut -d ' ' -f 1)" = "$ns_shipped" ]
}

ufw_active() {
	command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active$'
}

# check_ufw_policy warns when an active ufw lets in what no rule allows.
check_ufw_policy() {
	cu_policy=$(ufw status verbose | sed -n 's/^Default: \([a-z]*\) (incoming).*/\1/p')
	case $cu_policy in
	deny | reject) ;;
	*)
		FW_WARNINGS="${FW_WARNINGS}ufw's default incoming policy is ${cu_policy:-unknown}, so ports with no rule are open too. Pantalan leaves your policy as it is; \`sudo ufw default deny incoming\` changes it.
"
		warn "ufw's default incoming policy is ${cu_policy:-unknown}: ports with no rule are open too"
		;;
	esac
}

# resolve_firewall_mode: the stored PANTALAN_FIREWALL wins, and
# --no-firewall sets it on the first install. A flag that disagrees with the
# env file stops the install, as for the other settings.
resolve_firewall_mode() {
	rf_stored=$(env_setting PANTALAN_FIREWALL)
	case $rf_stored in
	'' | ufw | off) ;;
	*) die "PANTALAN_FIREWALL=$rf_stored in $ENV_FILE must be ufw or off. Edit pantalan.env, then run this again." ;;
	esac
	if [ -n "$OPT_NO_FIREWALL" ] && [ "$rf_stored" = ufw ]; then
		die "--no-firewall disagrees with PANTALAN_FIREWALL=ufw in $ENV_FILE. Nothing was changed. To leave the firewall alone from now on, edit pantalan.env: set PANTALAN_FIREWALL=off, then run this again without --no-firewall."
	fi
	FIREWALL_MODE=$rf_stored
	if [ -z "$FIREWALL_MODE" ]; then
		FIREWALL_MODE=ufw
		[ -z "$OPT_NO_FIREWALL" ] || FIREWALL_MODE=off
	fi
}

# firewall_package: the firewall package the install needs, if any.
firewall_package() {
	case $FW_ACTION in
	enable | add | stage) printf 'ufw\n' ;;
	esac
}

# detect_ssh_ports sets SSH_PORTS to the union of every source that knows
# an SSH port, since any one of them can be wrong: what sshd and ssh.socket
# listen on (an ssh.socket.d override can differ from sshd's config), the
# port lines of `sshd -T`, and the port this session came in on (sudo drops
# SSH_CONNECTION, so the session's sshd is found among the ancestors).
detect_ssh_ports() {
	ds_listening=$(port_set "$(sshd_listening_ports)")
	ds_config=$(port_set "$(sshd_config_ports)")
	ds_session=$(session_ssh_port)
	SSH_PORTS=$(port_set "$ds_listening $ds_config $ds_session")
	say "SSH: ports ${SSH_PORTS:-none found} (listening: ${ds_listening:-none}; sshd -T: ${ds_config:-none}; this session: ${ds_session:-not over SSH})."
}

# port_of ADDR: the port of ss's or systemd's ADDR:PORT (or a bare PORT).
port_of() {
	printf '%s\n' "${1##*:}"
}

# port_set PORTS...: the ports (separated by spaces or newlines), sorted,
# each once, on one line.
port_set() {
	printf '%s\n' "$*" | tr ' ' '\n' | grep -E '^[0-9]+$' | sort -n -u | tr '\n' ' ' | sed 's/ $//'
}

sshd_listening_ports() {
	{
		ss -Hltnp 2>/dev/null | awk '/\("sshd",/ { print $4 }'
		if systemctl is-active --quiet ssh.socket 2>/dev/null; then
			systemctl show --property Listen --value ssh.socket | awk '$2 == "(Stream)" { print $1 }'
		fi
	} | while read -r sl_addr; do port_of "$sl_addr"; done
}

sshd_config_ports() {
	sc_sshd=$(command -v sshd 2>/dev/null) || sc_sshd=/usr/sbin/sshd
	[ -x "$sc_sshd" ] || return 0
	# sshd -T refuses to run without /run/sshd, which only exists while
	# ssh.service runs: socket-activated SSH has none until the first login.
	# A private mount namespace provides one without touching the host.
	# shellcheck disable=SC2016 # $0 expands in the inner shell
	{
		"$sc_sshd" -T 2>/dev/null ||
			[ -d /run/sshd ] ||
			unshare --mount --propagation private \
				sh -c 'mount -t tmpfs tmpfs /run && mkdir /run/sshd && exec "$0" -T' "$sc_sshd" 2>/dev/null
	} | awk '$1 == "port" { print $2 }'
}

# session_ssh_port: the local port of the SSH connection this script runs
# under, if any.
session_ssh_port() {
	if [ -n "${SSH_CONNECTION:-}" ]; then
		# shellcheck disable=SC2086 # client-ip client-port server-ip server-port
		set -- $SSH_CONNECTION
		[ $# -lt 4 ] || {
			printf '%s\n' "$4"
			return 0
		}
	fi
	ss_pid=$$
	while [ "${ss_pid:-0}" -gt 1 ]; do
		case $(cat "/proc/$ss_pid/comm" 2>/dev/null) in
		sshd | sshd-session)
			ss_addr=$(ss -Htnp 2>/dev/null | awk -v p="pid=$ss_pid," '$1 == "ESTAB" && index($0, p) { print $4; exit }')
			if [ -n "$ss_addr" ]; then
				port_of "$ss_addr"
				return 0
			fi
			;;
		esac
		ss_pid=$(sed -n 's/^PPid:[[:space:]]*//p' "/proc/$ss_pid/status" 2>/dev/null)
	done
}

# comma_list PORTS...: "a, b, c", sorted.
comma_list() {
	port_set "$*" | sed 's/ /, /g'
}

# ufw_rules COMMENT: the ports `ufw allow PORT/tcp comment COMMENT` rules
# exist for, one per line.
ufw_rules() {
	ufw show added | sed -n "s|^ufw allow \([0-9]*\)/tcp comment '$1'\$|\1|p"
}

# ufw_has_rule PORT: is there a rule for exactly PORT/tcp, whatever its
# comment? Adding ours on top of an operator's would only rewrite their
# comment (ufw treats the comment as part of no rule's identity).
ufw_has_rule() {
	ufw show added | grep -Eq "^ufw allow $1/tcp( comment '.*')?\$"
}

configure_firewall() {
	cf_targets=$(port_set "80 443 $DASHBOARD_PORT")
	case $FW_ACTION in
	off | skip)
		FW_LINE="off (PANTALAN_FIREWALL=off)"
		[ "$FW_ACTION" = off ] || FW_LINE="skipped: $FW_REASON; open $(comma_list "$cf_targets") in it"
		say "Firewall: $FW_LINE"
		return 0
		;;
	esac
	# Rules from an earlier run make this an upgrade or repair, whose
	# summary says what changed.
	cf_ours=$(port_set "$(ufw_rules pantalan)")
	cf_had=$(port_set "$cf_ours $(ufw_rules pantalan-ssh)")
	cf_added=
	cf_taken=
	cf_removed=
	cf_ssh_added=
	# Added before anything is removed or enabled, so no moment is missing
	# a port.
	for cf_port in $cf_targets; do
		if in_list "$cf_port" "$cf_ours"; then
			continue
		elif ufw_has_rule "$cf_port"; then
			cf_taken="$cf_taken $cf_port"
		else
			quietly ufw allow "$cf_port/tcp" comment pantalan
			cf_added="$cf_added $cf_port"
		fi
	done
	for cf_port in $SSH_PORTS; do
		ufw_has_rule "$cf_port" && continue
		quietly ufw allow "$cf_port/tcp" comment pantalan-ssh
		cf_ssh_added="$cf_ssh_added $cf_port"
	done
	for cf_port in $cf_ours; do
		in_list "$cf_port" "$cf_targets" && continue
		quietly ufw --force delete allow "$cf_port/tcp"
		cf_removed="$cf_removed $cf_port"
	done
	case $FW_ACTION in
	enable)
		# --force: without it ufw asks y/n on stdin, which under curl | sh
		# is the script.
		quietly ufw --force enable
		confirm_ufw_active "$cf_targets $SSH_PORTS"
		;;
	add) confirm_ufw_active "$cf_targets $SSH_PORTS" ;;
	esac

	cf_allows="${SSH_PORTS:+SSH $(comma_list "$SSH_PORTS"), }$(comma_list "$cf_targets")"
	cf_added_text=$(comma_list "$cf_added")
	if [ -n "$cf_ssh_added" ]; then
		cf_added_text="${cf_added_text:+$cf_added_text and }SSH $(comma_list "$cf_ssh_added")"
	fi
	cf_change="${cf_added_text:+added $cf_added_text}"
	if [ -n "$cf_removed" ]; then
		cf_change="${cf_change:+$cf_change, }removed $(comma_list "$cf_removed")"
	fi
	case $FW_ACTION:${cf_had:+had} in
	enable:) FW_LINE="ufw enabled: allows $cf_allows" ;;
	enable:had) FW_LINE="ufw enabled: allows $cf_allows; ${cf_change:-unchanged}" ;;
	add:had) FW_LINE="ufw active: allows $cf_allows; ${cf_change:-unchanged}" ;;
	stage:*) FW_LINE="ufw NOT enabled ($FW_REASON): rules are staged${cf_had:+ (${cf_change:-unchanged})}; check SSH, then run: ufw --force enable" ;;
	add:)
		if [ -z "$cf_added_text" ]; then
			FW_LINE="ufw already active: your rules already allow $cf_allows, and are unchanged"
		else
			case $(port_set "$cf_taken") in
			*" "*) cf_set=were ;;
			*) cf_set=was ;;
			esac
			FW_LINE="ufw already active: added $cf_added_text${cf_taken:+ ($(comma_list "$cf_taken") $cf_set already allowed)}; your existing rules are unchanged"
		fi
		;;
	esac
	say "Firewall: $FW_LINE"
}

# in_list WORD LIST: is WORD one of LIST's space-separated words?
in_list() {
	case " $2 " in
	*" $1 "*) return 0 ;;
	esac
	return 1
}

# confirm_ufw_active PORTS: ufw reports itself active, with each of PORTS
# allowed.
confirm_ufw_active() {
	ca_status=$(ufw status)
	printf '%s\n' "$ca_status" | grep -q '^Status: active$' || die "ufw didn't come up: \`ufw status\` says $(printf '%s\n' "$ca_status" | head -n 1)"
	for ca_port in $1; do
		printf '%s\n' "$ca_status" | grep -q "^$ca_port/tcp " || die "ufw is active but \`ufw status\` doesn't show $ca_port/tcp allowed"
	done
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
	say "  Firewall        $FW_LINE"
	printf '%s' "$FW_WARNINGS" | while IFS= read -r ps_warning; do
		say "                  Warning: $ps_warning"
	done
	say ""
	say "  Cloud security groups are outside this server's firewall: open 80, 443 and $DASHBOARD_PORT there too."
	say "  Install log: $LOG_FILE"
}

# ---------------------------------------------------------------------------
# --uninstall and --purge. --uninstall removes what installing again puts
# back (service, unit, binary) and keeps everything else, so a reinstall
# brings it all back as it was. --purge removes the rest too. Docker, ufw
# and git are never removed: other things on the server may use them.

remove_pantalan() {
	step "Checking this host"
	check_systemd
	[ -z "$OPT_PURGE" ] || purge_preflight
	step "Removing the service"
	remove_service
	# Pantalan's firewall rules are removed here (the firewall ticket).
	if [ -n "$OPT_PURGE" ]; then
		step "Removing app containers and images"
		remove_apps
		step "Removing config, data and the service user"
		remove_files
		remove_service_user
		STEP="Printing the summary"
		print_purged
	else
		STEP="Printing the summary"
		print_uninstalled
	fi
}

# remove_service stops and disables the service and removes its unit and
# the binary. With nothing installed it does nothing, so it can re-run.
remove_service() {
	if [ -e "$UNIT_FILE" ]; then
		systemctl disable --now --quiet pantalan.service
		rm -f "$UNIT_FILE"
		systemctl daemon-reload
		systemctl reset-failed pantalan.service 2>/dev/null || :
		say "Stopped and disabled pantalan.service and removed $UNIT_FILE."
	fi
	if [ -e "$BIN" ] || [ -e "$BIN.new" ]; then
		rm -f "$BIN" "$BIN.new"
		say "Removed $BIN."
	fi
}

# app_containers: the IDs of Pantalan's app containers, running or not.
app_containers() { docker ps -aq --filter label=pantalan.app; }

print_uninstalled() {
	say ""
	say "Pantalan is uninstalled: its service, unit and $BIN are gone."
	say ""
	say "Kept, so installing again brings everything back as it was:"
	[ ! -d "$CONFIG_DIR" ] || say "  $CONFIG_DIR/  settings and the Admin Token"
	[ ! -d "$DATA_DIR" ] || say "  $DATA_DIR/  the database, TLS certificate and builds"
	[ ! -d "$DROPIN_DIR" ] || say "  $DROPIN_DIR/  your unit drop-ins"
	if docker_responds; then
		pu_count=$(app_containers | wc -l)
		say "  App containers (label pantalan.app, $pu_count now) and pantalan/* images, in Docker"
	fi
	! getent passwd "$SERVICE_USER" >/dev/null || say "  The $SERVICE_USER user"
	say "  Docker, ufw and git: the installer never removes them"
	say ""
	say "  Install again    curl -fsSL $INSTALL_URL | sudo sh"
	say "  Remove the rest  curl -fsSL $INSTALL_URL | sudo sh -s -- --purge"
	say "  Install log      $LOG_FILE"
}

# purge_preflight: a purge can't be undone, so it is typed out at the
# terminal, or given as --yes when there is none. Docker must answer, to
# remove the app containers; with no Docker there are none.
purge_preflight() {
	if command -v docker >/dev/null 2>&1 && ! docker_responds; then
		die "the Docker daemon doesn't answer, so the app containers and images can't be removed. Check \`systemctl status docker\`, then run this again. Nothing was changed."
	fi
	[ -z "$OPT_YES" ] || return 0
	have_tty || die "--purge deletes Pantalan's data, config, app containers and images and the $SERVICE_USER user, and there's no terminal to confirm it on. Nothing was changed. To purge without asking: curl -fsSL $INSTALL_URL | sudo sh -s -- --purge --yes"
	{
		say "--purge deletes, for good: $CONFIG_DIR (with the Admin Token), $DATA_DIR (the"
		say "database, the TLS certificate and builds), $DROPIN_DIR, Pantalan's app"
		say "containers and pantalan/* images, and the $SERVICE_USER user."
	} >/dev/tty
	pp_answer=$(ask "Type purge to delete all of it: ")
	[ "$pp_answer" = purge ] || die "the purge wasn't confirmed (typed \"$pp_answer\", not purge). Nothing was changed."
}

remove_apps() {
	command -v docker >/dev/null 2>&1 || return 0
	ra_ids=$(app_containers)
	if [ -n "$ra_ids" ]; then
		# shellcheck disable=SC2086 # one word per ID
		quietly docker rm --force --volumes $ra_ids
		say "Removed $(printf '%s\n' "$ra_ids" | wc -l) app container(s)."
	fi
	# By name, so an image that is also tagged as something else keeps
	# that tag.
	ra_images=$(docker images --filter 'reference=pantalan/*' --format '{{.Repository}}:{{.Tag}}')
	if [ -n "$ra_images" ]; then
		# shellcheck disable=SC2086 # one word per image
		quietly docker rmi $ra_images
		say "Removed $(printf '%s\n' "$ra_images" | wc -l) pantalan/* image(s)."
	fi
}

remove_files() {
	for rf_path in "$DROPIN_DIR" "$CONFIG_DIR" "$DATA_DIR"; do
		[ -e "$rf_path" ] || continue
		rm -rf "$rf_path"
		say "Removed $rf_path."
	done
	systemctl daemon-reload
}

remove_service_user() {
	if getent passwd "$SERVICE_USER" >/dev/null; then
		userdel "$SERVICE_USER"
		say "Removed the $SERVICE_USER user."
	fi
	if getent group "$SERVICE_USER" >/dev/null; then
		groupdel "$SERVICE_USER"
	fi
}

print_purged() {
	say ""
	say "Pantalan is purged: its service, binary, config, data, unit drop-ins, app"
	say "containers, pantalan/* images and the $SERVICE_USER user are gone."
	say "Docker, ufw and git are left as they are: the installer never removes them."
	say "  Install log      $LOG_FILE"
}

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
	if [ -n "$OPT_UNINSTALL$OPT_PURGE" ]; then
		remove_pantalan
		return 0
	fi

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
	step "Configuring the firewall"
	configure_firewall

	STEP="Printing the summary"
	stop_log
	print_summary "  $ADMIN_PASSWORD  (shown only now: sign in and keep it safe)"
	print_summary ": shown on screen only" >>"$LOG_FILE"
}

main "$@"
