#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# The installer functions are sourced below and call these stubs indirectly.
# shellcheck disable=SC2154,SC2329
set -eu
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

version=1.0.0
digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
other=sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
if sh scripts/stamp-install.sh "$version" "$tmp/missing-digest" >/dev/null 2>&1; then
	echo 'release installer accepted a missing digest' >&2
	exit 1
fi
if sh scripts/stamp-install.sh "$version" sha256:bad "$tmp/invalid-digest" >/dev/null 2>&1; then
	echo 'release installer accepted an invalid digest' >&2
	exit 1
fi
sh scripts/stamp-install.sh "$version" "$digest" "$tmp/install.sh" >/dev/null
grep -Fxq "KIT_IMAGE_DIGEST=$digest" "$tmp/install.sh"
if grep -q 'ABYSS_INSTALL_' "$tmp/install.sh"; then exit 1; fi

sed '/^COMMAND=install$/,$d' "$tmp/install.sh" >"$tmp/functions.sh"
(
	# shellcheck disable=SC1091
	. "$tmp/functions.sh"
	trap - EXIT
	repo=ghcr.io/iglace/abyssreader-server-kit
	[ "$IMAGE" = "$repo:$version@$digest" ]
	installed_tag "$IMAGE"
	[ "$current_tag" = "$version" ]
	installed_tag "$repo:0.9.0"
	[ "$current_tag" = 0.9.0 ]
	[ "$(upgrade_image "$version")" = "$IMAGE" ]

	curl() {
		for arg do url=$arg; done
		[ "$url" = 'https://github.com/IGlace/abyssreader-server-kit/releases/download/1.2.3/image-digest.txt' ] || return 1
		printf '%s\n' "$other"
	}
	[ "$(upgrade_image 1.2.3)" = "$repo:1.2.3@$other" ]
	if (curl() { echo invalid; }; upgrade_image 1.2.3) >/dev/null 2>&1; then exit 1; fi

	docker() {
		case "$1:$2" in
		image:inspect)
			[ "$3" = -f ] && [ "$5" = "$IMAGE" ] || return 1
			if [ -f "$tmp/pulled" ]; then
				printf '%s@%s\n' "$repo" "$digest"
			else
				printf '%s@%s\n' "$repo" "$other"
			fi ;;
		pull:*)
			[ "$2" = "$IMAGE" ] || return 1
			touch "$tmp/pulled" ;;
		*) return 1 ;;
		esac
	}
	pull_image "$IMAGE"
	test -f "$tmp/pulled"
	docker() {
		[ "$1:$2" = image:inspect ] || return 1
		printf '%s@%s\n' "$repo" "$digest"
	}
	pull_image "$IMAGE"
	docker() {
		[ "$1" = pull ] || printf '%s@%s\n' "$repo" "$other"
	}
	if (pull_image "$IMAGE") >/dev/null 2>&1; then
		echo 'installer accepted an image with the wrong digest after pulling' >&2
		exit 1
	fi
)
