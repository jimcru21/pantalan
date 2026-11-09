#!/bin/sh
# Builds Pantalan's release assets, the way .github/workflows/release.yml
# publishes them. Run it from a clean checkout of the tag:
#
#   deploy/release.sh check-tag TAG   exit 0 only for a plain vX.Y.Z tag
#   deploy/release.sh build TAG DIR   write the release assets into DIR
#
# The assets are pantalan_<tag>_linux_amd64, pantalan_<tag>_linux_arm64,
# install.sh with the tag baked into DEFAULT_VERSION, and SHA256SUMS over
# all three (sha256sum's own format, bare file names). The binaries embed
# the Dashboard's frontend, which build makes first, so it needs Node and
# npm (web/app/.nvmrc) as well as Go.
set -eu

ARCHES="amd64 arm64"
INSTALLER=deploy/install.sh

say() {
	printf 'release.sh: %s\n' "$*" >&2
}

die() {
	printf 'release.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat >&2 <<'EOF'
usage: deploy/release.sh check-tag TAG
       deploy/release.sh build TAG DIR
EOF
	exit 2
}

# check_tag TAG: releases are cut only from vX.Y.Z tags, with no
# pre-release or build suffix, so the installer's `sort -V` comparison of
# versions stays correct.
check_tag() {
	# The first arm catches what the second's globs would let through:
	# other characters (a suffix, a newline), a fourth part, an empty part.
	case $1 in
	v*[!0-9.]* | v*.*.*.* | v*..* | v*. ) ;;
	v[0-9]*.[0-9]*.[0-9]*) return 0 ;;
	esac
	die "'$1' is not a release tag: releases are cut only from plain vX.Y.Z tags (no pre-releases)"
}

# build TAG DIR: the release assets for TAG into DIR, from the checkout this
# script is in. Go stamps the version into the binaries from the tag (no
# ldflags), so the checkout must be clean and at TAG.
build() {
	b_tag=$1
	b_dir=$2
	# The version check runs a binary, so it has to be one this host runs.
	b_host=$(go env GOHOSTOS)/$(go env GOHOSTARCH)
	case $b_host in
	linux/amd64 | linux/arm64) ;;
	*) die "build on linux/amd64 or linux/arm64, not $b_host: the version check runs the binary" ;;
	esac
	[ "$(grep -c '@VERSION@' "$INSTALLER")" -eq 1 ] ||
		die "$INSTALLER must hold exactly one @VERSION@ placeholder (DEFAULT_VERSION=@VERSION@)"
	[ -z "$(ls -A "$b_dir")" ] || die "$b_dir isn't empty"

	# The Dashboard's frontend first, into web/dist, which the binaries
	# embed. npm ci installs exactly the lockfile, and --ignore-scripts
	# keeps any package from running code at install.
	command -v npm >/dev/null || die "npm not found: building the Dashboard's frontend needs Node (web/app/.nvmrc)"
	say "building the Dashboard's frontend"
	(
		cd web/app
		npm ci --ignore-scripts
		npm run build
	) >&2
	[ -f web/dist/index.html ] || die "the frontend build wrote no web/dist/index.html"

	b_assets=install.sh
	for b_arch in $ARCHES; do
		say "building pantalan_${b_tag}_linux_$b_arch"
		CGO_ENABLED=0 GOOS=linux GOARCH=$b_arch \
			go build -trimpath -ldflags="-s -w" -o "$b_dir/pantalan_${b_tag}_linux_$b_arch" ./cmd/pantalan
		b_assets="$b_assets pantalan_${b_tag}_linux_$b_arch"
	done

	b_bin=pantalan_${b_tag}_linux_${b_host#linux/}
	b_reports=$("$b_dir/$b_bin" version)
	[ "$b_reports" = "$b_tag" ] ||
		die "$b_bin version printed '$b_reports', want $b_tag: build from a clean checkout of the tag"
	say "$b_bin version: $b_reports"

	sed "s/@VERSION@/$b_tag/" "$INSTALLER" >"$b_dir/install.sh"
	if grep -q '@VERSION@' "$b_dir/install.sh"; then
		die "the @VERSION@ placeholder survived in install.sh"
	fi
	grep -qx "DEFAULT_VERSION=$b_tag" "$b_dir/install.sh" ||
		die "install.sh doesn't set DEFAULT_VERSION=$b_tag"

	(
		cd "$b_dir"
		# shellcheck disable=SC2086 # file names without spaces, one per word
		sha256sum $b_assets >SHA256SUMS
		sha256sum -c --strict --quiet SHA256SUMS
	)
	say "wrote the release assets for $b_tag to $b_dir"
}

main() {
	[ $# -ge 1 ] || usage
	cmd=$1
	shift
	case $cmd in
	check-tag)
		[ $# -eq 1 ] || usage
		check_tag "$1"
		;;
	build)
		[ $# -eq 2 ] || usage
		check_tag "$1"
		mkdir -p "$2"
		m_dir=$(cd "$2" && pwd)
		cd "$(dirname "$0")/.."
		build "$1" "$m_dir"
		;;
	*) usage ;;
	esac
}

main "$@"
