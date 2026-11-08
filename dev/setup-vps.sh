#!/bin/sh
# Sets up a fresh Debian or Ubuntu VPS for Pantalan development: the tools
# to build, test and run it from a checkout, on the host or with the Docker
# dev stack (compose.yaml).
#
#   curl -fL --max-time 120 -o setup-vps.sh https://raw.githubusercontent.com/jimcru21/pantalan/main/dev/setup-vps.sh
#   sudo sh setup-vps.sh [--user alice] [--clone ~/pantalan]
#
# or, from a checkout: sudo sh dev/setup-vps.sh
#
# It installs:
#
#   packages  git, make, gcc (go test -race needs cgo), curl, jq, shellcheck
#   Go        the newest patch release of the go.mod's Go minor version,
#             from go.dev, checksum-verified, in /usr/local/go
#   Docker    Docker CE with the buildx and compose plugins, from Docker's
#             apt repository; the user joins the docker group
#   gh        the GitHub CLI, from GitHub's apt repository (the issue
#             tracker, PRs)
#
# Every step converges, so running it again repairs or upgrades: Go moves to
# the newest patch release, the rest is left as it is. It never touches the
# firewall, and never removes a Docker it didn't install.
set -eu

REPO_URL=https://github.com/jimcru21/pantalan
# The Go minor version used when there's no go.mod to read it from.
DEFAULT_GO_MINOR=1.26
GO_ROOT=/usr/local/go
KEYRINGS=/etc/apt/keyrings

# Flags, as given.
OPT_USER=
OPT_CLONE=
OPT_GO_VERSION=

# What the steps found out.
DEV_USER=
DEV_HOME=
OS_ID=
OS_CODENAME=
OS_NAME=
ARCH=
TMP_DIR=
STEP=checks

# ---------------------------------------------------------------------------
# Helpers

say() { printf '%s\n' "$*"; }

warn() { printf 'setup-vps: warning: %s\n' "$*" >&2; }

die() {
	printf 'setup-vps: %s\n' "$*" >&2
	exit 1
}

step() {
	STEP=$1
	say "==> $1"
}

usage() {
	cat <<EOF
Set up this Debian or Ubuntu server for Pantalan development: Go, Docker
CE (with Compose), git, make, gcc, shellcheck, jq and the GitHub CLI.

  sudo sh setup-vps.sh [options]

Options:
  --user NAME         the developer who joins the docker group and owns the
                      clone (default: the user who ran sudo)
  --clone DIR         also clone $REPO_URL into DIR
  --go-version X.Y.Z  install this Go release instead of the newest patch
                      release of the go.mod's minor version
  -h, --help          show this help
EOF
}

pkg_installed() {
	dpkg-query -W -f='${db:Status-Abbrev}' "$1" 2>/dev/null | grep -q '^ii'
}

os_field() {
	sed -n "s/^$1=//p" /etc/os-release | sed 's/^"\(.*\)"$/\1/' | head -n 1
}

apt_install() {
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends "$@"
}

# quietly CMD...: runs CMD with its (long) output hidden; when it fails, its
# last lines are shown.
quietly() {
	q_code=0
	"$@" >"$TMP_DIR/quietly.out" 2>&1 || q_code=$?
	if [ "$q_code" -ne 0 ]; then
		tail -n 25 "$TMP_DIR/quietly.out" >&2
		die "$* failed (exit $q_code)"
	fi
}

fetch() {
	curl -fsSL --retry 3 --connect-timeout 20 --max-time 600 -o "$2" "$1" ||
		die "couldn't download $1"
}

# as_dev CMD...: runs CMD as the developer, with their home.
as_dev() {
	if [ "$DEV_USER" = root ]; then
		"$@"
	else
		runuser -u "$DEV_USER" -- env HOME="$DEV_HOME" "$@"
	fi
}

docker_responds() { docker version --format '{{.Server.Version}}' >/dev/null 2>&1; }

on_exit() {
	oe_code=$?
	if [ "$oe_code" -ne 0 ]; then
		printf '\nsetup-vps: failed during "%s".\n' "$STEP" >&2
		printf 'Fix the problem above and run the same command again: it picks up where it stopped.\n' >&2
	fi
	[ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
	exit "$oe_code"
}

# ---------------------------------------------------------------------------
# Arguments and checks: nothing on the host changes before these pass.

parse_args() {
	while [ $# -gt 0 ]; do
		case $1 in
		--user)
			[ $# -ge 2 ] || die "--user needs a username"
			OPT_USER=$2
			shift 2
			;;
		--clone)
			[ $# -ge 2 ] || die "--clone needs a directory"
			OPT_CLONE=$2
			shift 2
			;;
		--go-version)
			[ $# -ge 2 ] || die "--go-version needs a version, like 1.26.1"
			OPT_GO_VERSION=${2#go}
			printf '%s\n' "$OPT_GO_VERSION" | grep -Eq '^[0-9]+\.[0-9]+(\.[0-9]+)?$' ||
				die "--go-version $2 isn't a Go version, like 1.26.1"
			shift 2
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown option: $1"
			;;
		esac
	done
}

check_root() {
	[ "$(id -u)" -eq 0 ] || die "run this as root: sudo sh $0"
}

check_os() {
	[ -r /etc/os-release ] || die "can't read /etc/os-release; this needs Debian or Ubuntu"
	OS_ID=$(os_field ID)
	OS_CODENAME=$(os_field VERSION_CODENAME)
	OS_NAME=$(os_field PRETTY_NAME)
	case $OS_ID in
	debian | ubuntu) ;;
	*) die "${OS_NAME:-this distro} isn't supported: this needs Debian or Ubuntu" ;;
	esac
	[ -n "$OS_CODENAME" ] || die "can't tell which $OS_ID release this is (no VERSION_CODENAME in /etc/os-release)"
	case $OS_ID/$OS_CODENAME in
	debian/bookworm | debian/trixie | ubuntu/noble | ubuntu/resolute) ;;
	*) warn "$OS_NAME isn't one Pantalan supports (Debian 12 or 13, Ubuntu 24.04 or 26.04 LTS); carrying on" ;;
	esac
	command -v apt-get >/dev/null 2>&1 || die "apt-get is missing"
	ARCH=$(dpkg --print-architecture)
	case $ARCH in
	amd64 | arm64) ;;
	*) die "the $ARCH architecture isn't supported: this needs amd64 or arm64" ;;
	esac
	say "System: $OS_NAME ($ARCH)"
}

resolve_user() {
	DEV_USER=${OPT_USER:-${SUDO_USER:-root}}
	getent passwd "$DEV_USER" >/dev/null || die "there's no user $DEV_USER; create it first (adduser $DEV_USER) or pass --user"
	DEV_HOME=$(getent passwd "$DEV_USER" | cut -d: -f6)
	say "Developer: $DEV_USER ($DEV_HOME)"
	if [ "$DEV_USER" = root ]; then
		warn "developing as root; consider a normal user (adduser alice, then sudo sh $0 --user alice)"
	fi
}

# check_docker refuses a Docker that conflicts with Docker CE's packages:
# replacing it is the developer's call.
check_docker() {
	pkg_installed docker-ce && return 0
	for cd_pkg in docker.io podman-docker docker-engine docker-compose; do
		if pkg_installed "$cd_pkg"; then
			die "$cd_pkg is installed and conflicts with Docker CE. Remove it (apt-get remove $cd_pkg; see https://docs.docker.com/engine/install/$OS_ID/), then run this again."
		fi
	done
	if [ -e /snap/bin/docker ]; then
		die "Docker from snap is installed. Remove it (snap remove docker), then run this again."
	fi
	if docker_responds; then
		die "a Docker that isn't Docker CE from Docker's apt repository is installed. Remove it, then run this again."
	fi
}

check_clone_dir() {
	[ -n "$OPT_CLONE" ] || return 0
	[ -e "$OPT_CLONE" ] || return 0
	[ -d "$OPT_CLONE/.git" ] && return 0
	if [ ! -d "$OPT_CLONE" ] || [ -n "$(ls -A "$OPT_CLONE")" ]; then
		die "--clone $OPT_CLONE exists and isn't a git checkout or an empty directory"
	fi
}

# ---------------------------------------------------------------------------
# Steps

install_packages() {
	step "packages"
	quietly apt-get update
	quietly apt_install ca-certificates curl git make build-essential jq shellcheck
	say "Installed git, make, gcc, curl, jq and shellcheck."
}

clone_repo() {
	[ -n "$OPT_CLONE" ] || return 0
	step "clone"
	if [ -d "$OPT_CLONE/.git" ]; then
		say "$OPT_CLONE is already a checkout; leaving it as it is."
		return 0
	fi
	# git makes the directory (and its parents) as the developer.
	quietly as_dev git clone "$REPO_URL.git" "$OPT_CLONE"
	say "Cloned $REPO_URL into $OPT_CLONE."
}

# go_mod: the go.mod to read the Go version from: this script's checkout,
# else the clone, else none.
go_mod() {
	# Piped into sh, $0 is the shell, not this file.
	gm_self=
	[ -f "$0" ] && gm_self=$(dirname "$0")/../go.mod
	for gm_file in "$gm_self" "${OPT_CLONE:+$OPT_CLONE/go.mod}"; do
		[ -n "$gm_file" ] && [ -f "$gm_file" ] && { printf '%s\n' "$gm_file"; return 0; }
	done
	return 0
}

install_go() {
	step "Go"
	if [ -n "$OPT_GO_VERSION" ]; then
		ig_want="go$OPT_GO_VERSION"
		ig_filter='.version == $want'
	else
		ig_minor=$DEFAULT_GO_MINOR
		ig_mod=$(go_mod)
		if [ -n "$ig_mod" ]; then
			ig_minor=$(sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' "$ig_mod" | head -n 1)
			[ -n "$ig_minor" ] || die "can't read the Go version from $ig_mod"
		fi
		ig_want="go$ig_minor"
		ig_filter='.stable and (.version | test("^" + ($want | gsub("\\."; "\\.")) + "(\\.[0-9]+)?$"))'
	fi

	fetch "https://go.dev/dl/?mode=json&include=all" "$TMP_DIR/go.json"
	ig_line=$(jq -r --arg want "$ig_want" --arg arch "$ARCH" "
		[.[] | select($ig_filter)] | first // empty
		| .version as \$v
		| .files[] | select(.os == \"linux\" and .arch == \$arch and .kind == \"archive\")
		| \"\(\$v) \(.filename) \(.sha256)\"" "$TMP_DIR/go.json" | head -n 1)
	[ -n "$ig_line" ] || die "go.dev has no linux/$ARCH release matching $ig_want"
	ig_version=${ig_line%% *}
	ig_rest=${ig_line#* }
	ig_file=${ig_rest%% *}
	ig_sum=${ig_rest#* }

	if [ -x "$GO_ROOT/bin/go" ] && [ "$("$GO_ROOT/bin/go" env GOVERSION 2>/dev/null)" = "$ig_version" ]; then
		say "$ig_version is already installed in $GO_ROOT."
	else
		say "Installing $ig_version into $GO_ROOT"
		fetch "https://go.dev/dl/$ig_file" "$TMP_DIR/$ig_file"
		printf '%s  %s\n' "$ig_sum" "$TMP_DIR/$ig_file" | sha256sum -c --status ||
			die "$ig_file doesn't match go.dev's checksum"
		rm -rf "$GO_ROOT"
		tar -C "$(dirname "$GO_ROOT")" -xzf "$TMP_DIR/$ig_file"
	fi

	# Login shells get go, and what `go install` puts in ~/go/bin.
	cat >/etc/profile.d/go.sh <<'EOF'
# Written by Pantalan's dev/setup-vps.sh.
case ":$PATH:" in
*:/usr/local/go/bin:*) ;;
*) PATH=$PATH:/usr/local/go/bin:$HOME/go/bin ;;
esac
export PATH
EOF
	# Non-login shells (a new terminal tab, tmux) on Debian and Ubuntu read
	# ~/.bashrc only.
	ig_rc=$DEV_HOME/.bashrc
	if [ -f "$ig_rc" ] && ! grep -q '/etc/profile.d/go.sh' "$ig_rc"; then
		printf '\n# Go, added by Pantalan dev/setup-vps.sh\n[ -r /etc/profile.d/go.sh ] && . /etc/profile.d/go.sh\n' >>"$ig_rc"
	fi
	say "$("$GO_ROOT/bin/go" version)"
}

# install_docker follows https://docs.docker.com/engine/install/debian/ (and
# /ubuntu/): Docker's key and deb822 source, then its packages.
install_docker() {
	step "Docker"
	if pkg_installed docker-ce; then
		say "docker-ce $(dpkg-query -W -f='${Version}' docker-ce) is already installed; leaving it as it is."
		for id_pkg in docker-buildx-plugin docker-compose-plugin; do
			pkg_installed "$id_pkg" || quietly apt_install "$id_pkg"
		done
	else
		say "Installing Docker CE from Docker's apt repository"
		install -m 0755 -d "$KEYRINGS"
		fetch "https://download.docker.com/linux/$OS_ID/gpg" "$KEYRINGS/docker.asc"
		chmod a+r "$KEYRINGS/docker.asc"
		cat >/etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/$OS_ID
Suites: $OS_CODENAME
Components: stable
Architectures: $ARCH
Signed-By: $KEYRINGS/docker.asc
EOF
		quietly apt-get update
		quietly apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
	fi
	quietly systemctl enable --now docker.service
	id_i=0
	until docker_responds; do
		id_i=$((id_i + 1))
		[ "$id_i" -lt 30 ] || die "Docker doesn't answer. Check \`systemctl status docker\`."
		sleep 1
	done
	if [ "$DEV_USER" != root ] && ! id -nG "$DEV_USER" | tr ' ' '\n' | grep -qx docker; then
		usermod -aG docker "$DEV_USER"
		say "Added $DEV_USER to the docker group (takes effect at their next login)."
	fi
	say "Docker $(docker version --format '{{.Server.Version}}'), $(docker compose version)"
}

# install_gh follows https://github.com/cli/cli/blob/trunk/docs/install_linux.md.
install_gh() {
	step "GitHub CLI"
	if ! pkg_installed gh; then
		install -m 0755 -d "$KEYRINGS"
		fetch https://cli.github.com/packages/githubcli-archive-keyring.gpg "$KEYRINGS/githubcli-archive-keyring.gpg"
		chmod a+r "$KEYRINGS/githubcli-archive-keyring.gpg"
		printf 'deb [arch=%s signed-by=%s] https://cli.github.com/packages stable main\n' \
			"$ARCH" "$KEYRINGS/githubcli-archive-keyring.gpg" >/etc/apt/sources.list.d/github-cli.list
		quietly apt-get update
		quietly apt_install gh
	fi
	say "$(gh --version | head -n 1)"
}

summary() {
	STEP=summary
	sm_dir=${OPT_CLONE:-}
	if [ -z "$sm_dir" ] && [ -f "$0" ] && [ -f "$(dirname "$0")/../go.mod" ]; then
		sm_dir=$(cd "$(dirname "$0")/.." && pwd)
	fi
	cat <<EOF

Done. This server has Go, Docker CE with Compose, git, make, gcc,
shellcheck, jq and gh.

Next, as $DEV_USER:

  1. Log out and back in, so the docker group and Go's PATH apply.
EOF
	if [ -n "$sm_dir" ]; then
		say "  2. cd $sm_dir"
	else
		say "  2. git clone $REPO_URL.git && cd pantalan"
	fi
	cat <<EOF
  3. Sign in to GitHub for the issue tracker and PRs: gh auth login
  4. Start developing, either way:
       docker compose up    the dev stack: rebuilds on every change
       make check           vet, race tests and gofmt on the host

To open the dev stack's Dashboard from your own browser, see "On a VPS"
under "Develop with Docker" in the README: set PANTALAN_DEV_BIND=0.0.0.0 in
.env and open port 8443 in your provider's firewall, ideally to your IP
only (Docker's published ports bypass ufw).
EOF
}

main() {
	parse_args "$@"
	check_root
	check_os
	resolve_user
	check_docker
	check_clone_dir
	TMP_DIR=$(mktemp -d /tmp/pantalan-setup-vps.XXXXXX)
	trap on_exit EXIT
	trap 'exit 130' INT TERM
	install_packages
	clone_repo
	install_go
	install_docker
	install_gh
	summary
}

main "$@"
