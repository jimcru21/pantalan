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
# and, for the developer (in their home, unless --no-agent-tools), the tools
# Claude Code works on the repo with:
#
#   Claude Code  the native installer, in ~/.local/bin (updates itself)
#   colgrep      LightOn's semantic code search, in ~/.cargo/bin, with its
#                Claude Code hooks
#   semble       MinishLab's code search, with uv, as a Claude Code MCP
#                server and sub-agent
#   skills       Matt Pocock's skills, from a checkout of the newest release
#                of github.com/mattpocock/skills, linked into
#                ~/.claude/skills (not the Claude Code plugin, which lags
#                behind the releases); docs/agents already holds their
#                per-repo setup
#
# Every step converges, so running it again repairs or upgrades: Go, colgrep
# semble and the skills move to their newest releases, the rest is left as it is. It
# never touches the firewall, and never removes a Docker it didn't install.
set -eu

REPO_URL=https://github.com/jimcru21/pantalan
SKILLS_URL=https://github.com/mattpocock/skills
# The Go minor version used when there's no go.mod to read it from.
DEFAULT_GO_MINOR=1.26
GO_ROOT=/usr/local/go
KEYRINGS=/etc/apt/keyrings

# Flags, as given.
OPT_USER=
OPT_CLONE=
OPT_GO_VERSION=
OPT_NO_AGENT_TOOLS=

# What the steps found out.
DEV_USER=
DEV_HOME=
OS_ID=
OS_CODENAME=
OS_NAME=
ARCH=
TMP_DIR=
STEP=checks
AGENT_WARNINGS=  # the agent tools' integrations that failed, for the summary

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
CE (with Compose), git, make, gcc, shellcheck, jq and the GitHub CLI, plus
Claude Code with colgrep, semble and Matt Pocock's skills.

  sudo sh setup-vps.sh [options]

Options:
  --user NAME         the developer who joins the docker group and owns the
                      clone (default: the user who ran sudo)
  --clone DIR         also clone $REPO_URL into DIR
  --go-version X.Y.Z  install this Go release instead of the newest patch
                      release of the go.mod's minor version
  --no-agent-tools    skip Claude Code, colgrep, semble and the skills
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

# dev_sh SCRIPT [ARG...]: runs SCRIPT with sh as the developer, in their
# home, with the PATH their login shell gets and no terminal to prompt on.
# The ARGs are SCRIPT's $1, $2, ...
dev_sh() {
	ds_script=$1
	shift
	ds_path=$DEV_HOME/.local/bin:$DEV_HOME/.cargo/bin:$GO_ROOT/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
	if [ "$DEV_USER" = root ]; then
		(cd "$DEV_HOME" && env HOME="$DEV_HOME" PATH="$ds_path" sh -c "$ds_script" sh "$@") </dev/null
	else
		(cd "$DEV_HOME" && runuser -u "$DEV_USER" -- env HOME="$DEV_HOME" USER="$DEV_USER" \
			LOGNAME="$DEV_USER" PATH="$ds_path" sh -c "$ds_script" sh "$@") </dev/null
	fi
}

# agent_try WHAT SCRIPT: dev_sh SCRIPT, where a failure is a warning in the
# summary rather than the end of the run: these integrations depend on
# third-party installers that can change under us.
agent_try() {
	at_code=0
	dev_sh "$2" >"$TMP_DIR/agent.out" 2>&1 || at_code=$?
	if [ "$at_code" -ne 0 ]; then
		tail -n 15 "$TMP_DIR/agent.out" >&2
		warn "$1 failed (exit $at_code)"
		AGENT_WARNINGS="$AGENT_WARNINGS
  - $1 failed; as $DEV_USER, run: $2"
		return 1
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
		--no-agent-tools)
			OPT_NO_AGENT_TOOLS=1
			shift
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
		# shellcheck disable=SC2016 # $want is jq's, bound by --arg
		ig_filter='.version == $want'
	else
		ig_minor=$DEFAULT_GO_MINOR
		ig_mod=$(go_mod)
		if [ -n "$ig_mod" ]; then
			ig_minor=$(sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' "$ig_mod" | head -n 1)
			[ -n "$ig_minor" ] || die "can't read the Go version from $ig_mod"
		fi
		ig_want="go$ig_minor"
		# shellcheck disable=SC2016 # $want is jq's, bound by --arg
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

	install_profile
	say "$("$GO_ROOT/bin/go" version)"
}

# install_profile puts go, what `go install` builds (~/go/bin), and the agent
# tools (~/.local/bin, ~/.cargo/bin) on login shells' PATH.
install_profile() {
	cat >/etc/profile.d/pantalan-dev.sh <<'EOF'
# Written by Pantalan's dev/setup-vps.sh.
for _pantalan_dir in /usr/local/go/bin "$HOME/go/bin" "$HOME/.local/bin" "$HOME/.cargo/bin"; do
	case ":$PATH:" in
	*":$_pantalan_dir:"*) ;;
	*) PATH=$PATH:$_pantalan_dir ;;
	esac
done
unset _pantalan_dir
export PATH
EOF
	# Non-login shells (a new terminal tab, tmux) on Debian and Ubuntu read
	# ~/.bashrc only.
	ip_rc=$DEV_HOME/.bashrc
	if [ -f "$ip_rc" ] && ! grep -q '/etc/profile.d/pantalan-dev.sh' "$ip_rc"; then
		printf '\n# Go and the agent tools, added by Pantalan dev/setup-vps.sh\n[ -r /etc/profile.d/pantalan-dev.sh ] && . /etc/profile.d/pantalan-dev.sh\n' >>"$ip_rc"
	fi
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

# install_agent_tools installs, as the developer, Claude Code and the tools
# it searches and plans with, from each project's documented installer:
# https://code.claude.com/docs/en/setup, https://github.com/lightonai/next-plaid/tree/main/colgrep,
# https://github.com/MinishLab/semble and https://github.com/mattpocock/skills.
# The binaries are required; their Claude Code integrations only warn.
install_agent_tools() {
	[ -z "$OPT_NO_AGENT_TOOLS" ] || return 0
	step "agent tools"

	if [ -x "$DEV_HOME/.local/bin/claude" ]; then
		say "Claude Code is already installed; it updates itself."
	else
		say "Installing Claude Code"
		quietly dev_sh 'curl -fsSL https://claude.ai/install.sh | bash'
	fi
	say "Claude Code $(dev_sh 'claude --version' 2>/dev/null | head -n 1)"

	# colgrep's installer always fetches the latest release. Its
	# --install-agent (a ~2.5 GB local model) is left for the developer.
	say "Installing colgrep"
	quietly dev_sh "curl --proto '=https' --tlsv1.2 -LsSf https://github.com/lightonai/next-plaid/releases/latest/download/colgrep-installer.sh | sh"
	agent_try "colgrep's Claude Code hooks" 'colgrep --install-claude-code' || :
	say "$(dev_sh 'colgrep --version' 2>/dev/null | head -n 1)"

	if [ ! -x "$DEV_HOME/.local/bin/uv" ]; then
		say "Installing uv (for semble)"
		quietly dev_sh 'curl -LsSf https://astral.sh/uv/install.sh | sh'
	fi
	say "Installing semble"
	if dev_sh 'uv tool list' 2>/dev/null | grep -q '^semble '; then
		quietly dev_sh 'uv tool upgrade semble'
	else
		quietly dev_sh 'uv tool install semble'
	fi
	# Re-run after every upgrade: it pins the MCP server to the installed
	# version.
	agent_try "semble's Claude Code MCP server and sub-agent" 'semble install --agent claude --type mcp subagent --yes' || :
	say "$(dev_sh 'uv tool list' 2>/dev/null | grep '^semble ')"

	install_skills
}

# install_skills checks out the newest release tag of Matt Pocock's skills
# and links the skills that release publishes (the plugin manifest's list,
# which leaves out in-progress/ and misc/) into ~/.claude/skills, where
# Claude Code finds them in every project. The plugin in Claude Code's
# official marketplace trails the releases, so it isn't used.
install_skills() {
	is_dir=$DEV_HOME/.local/share/mattpocock-skills
	is_tag=$(git -c versionsort.suffix=- ls-remote --tags --refs --sort=-v:refname "$SKILLS_URL" 'v*' |
		sed -n '1s#.*refs/tags/##p') || is_tag=
	[ -n "$is_tag" ] || die "couldn't find a release tag in $SKILLS_URL"
	if [ -d "$is_dir/.git" ]; then
		# shellcheck disable=SC2016 # expands in the developer's shell
		quietly dev_sh 'git -C "$1" fetch --quiet --tags --force origin && git -C "$1" checkout --quiet --force --detach "refs/tags/$2"' "$is_dir" "$is_tag"
	else
		# shellcheck disable=SC2016 # expands in the developer's shell
		quietly dev_sh 'mkdir -p "$(dirname "$1")" && git -c advice.detachedHead=false clone --quiet --branch "$2" "$3.git" "$1"' "$is_dir" "$is_tag" "$SKILLS_URL"
	fi
	# Links into the checkout that this release no longer lists are
	# dropped; a skill of the developer's own with the same name is kept.
	# shellcheck disable=SC2016 # expands in the developer's shell
	is_kept=$(dev_sh '
		set -eu
		dir=$1
		skills=$HOME/.claude/skills
		mkdir -p "$skills"
		for link in "$skills"/*; do
			[ -L "$link" ] || continue
			case $(readlink "$link") in "$dir"/*) rm -f "$link" ;; esac
		done
		jq -r ".skills[]" "$dir/.claude-plugin/plugin.json" | while IFS= read -r path; do
			target=$dir/${path#./}
			name=${path##*/}
			[ -f "$target/SKILL.md" ] || continue
			if [ -e "$skills/$name" ] || [ -L "$skills/$name" ]; then
				echo "$skills/$name"
				continue
			fi
			ln -s "$target" "$skills/$name"
		done' "$is_dir") || die "couldn't link Matt Pocock's skills into $DEV_HOME/.claude/skills"
	for is_path in $is_kept; do
		warn "$is_path isn't a link to Matt Pocock's skills, so it's kept and that skill isn't linked"
	done
	# shellcheck disable=SC2016 # expands in the developer's shell
	is_count=$(dev_sh 'find "$HOME/.claude/skills" -maxdepth 1 -type l -lname "$1/*" | wc -l' "$is_dir")
	say "Matt Pocock's skills $is_tag: $is_count linked into $DEV_HOME/.claude/skills."
	if dev_sh 'claude plugin list' 2>/dev/null | grep -q 'mattpocock-skills'; then
		warn "the mattpocock-skills plugin is installed too, so Claude Code sees every skill twice"
		AGENT_WARNINGS="$AGENT_WARNINGS
  - the mattpocock-skills plugin duplicates the linked skills; as $DEV_USER, run: claude plugin uninstall mattpocock-skills"
	fi
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
EOF
	if [ -z "$OPT_NO_AGENT_TOOLS" ]; then
		say "$DEV_USER has Claude Code, colgrep, semble and Matt Pocock's skills."
		if [ -n "$AGENT_WARNINGS" ]; then
			say "Some Claude Code integrations didn't install:$AGENT_WARNINGS"
		fi
	fi
	cat <<EOF

Next, as $DEV_USER:

  1. Log out and back in, so the docker group and the new PATH apply.
EOF
	if [ -n "$sm_dir" ]; then
		say "  2. cd $sm_dir"
	else
		say "  2. git clone $REPO_URL.git && cd pantalan"
	fi
	cat <<EOF
  3. Sign in to GitHub for the issue tracker and PRs: gh auth login
EOF
	if [ -z "$OPT_NO_AGENT_TOOLS" ]; then
		say "     and to Claude Code: run claude in the checkout and follow the prompts"
	fi
	cat <<EOF
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
	install_agent_tools
	summary
}

main "$@"
