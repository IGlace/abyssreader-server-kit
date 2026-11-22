#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
#
# Stamp a release installer with the exact OCI image digest published by the release workflow.
# Local installer fixtures can use a tag, with or without the development hooks.
set -eu
cd "$(dirname "$0")/.."
keep_hooks=""
digest=""
case "${1:-}" in
--keep-test-hooks) keep_hooks=1; digest=local-test; shift ;;
--local-tag) digest=local-test; shift ;;
esac
version=${1:-}
usage() {
	echo "usage: scripts/stamp-install.sh VERSION sha256:DIGEST [OUT]" >&2
	exit 2
}
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'; then
	usage
fi
# Prevent multiline arguments from making grep match only one of their lines.
case "$version" in *[!0-9A-Za-z.-]*) usage ;; esac
shift
if [ -z "$digest" ]; then
	digest=${1:-}
	case "$digest" in sha256:*) hex=${digest#sha256:} ;; *) usage ;; esac
	case "$hex" in *[!0-9a-f]*) usage ;; esac
	[ "${#hex}" -eq 64 ] || usage
	shift
fi
[ "$#" -le 1 ] || usage
out=${1:-dist/install.sh}
if [ "$(grep -c '^KIT_VERSION=unreleased$' install.sh)" != 1 ] ||
   [ "$(grep -c '^KIT_IMAGE_DIGEST=unreleased$' install.sh)" != 1 ]; then
	echo "install.sh must have one version and one digest marker" >&2
	exit 1
fi
if [ "$(grep -c '^# BEGIN TEST HOOKS' install.sh)" != 1 ] ||
   [ "$(grep -c '^# END TEST HOOKS$' install.sh)" != 1 ]; then
	echo "install.sh must have exactly one TEST HOOKS block" >&2
	exit 1
fi
mkdir -p "$(dirname "$out")"
tmp="$out.tmp.$$"
trap 'rm -f "$tmp"' EXIT
if [ -n "$keep_hooks" ]; then
	sed -e "s/^KIT_VERSION=unreleased\$/KIT_VERSION=$version/" \
		-e "s/^KIT_IMAGE_DIGEST=unreleased\$/KIT_IMAGE_DIGEST=$digest/" install.sh >"$tmp"
else
	sed -e "s/^KIT_VERSION=unreleased\$/KIT_VERSION=$version/" \
		-e "s/^KIT_IMAGE_DIGEST=unreleased\$/KIT_IMAGE_DIGEST=$digest/" \
		-e '/^# BEGIN TEST HOOKS/,/^# END TEST HOOKS$/d' install.sh >"$tmp"
	if grep -q 'ABYSS_INSTALL_\(DIR\|WAIT\|TEST\)' "$tmp"; then
		echo "the release copy still reads a test hook" >&2
		exit 1
	fi
fi
grep -Fxq "KIT_VERSION=$version" "$tmp"
grep -Fxq "KIT_IMAGE_DIGEST=$digest" "$tmp"
chmod 644 "$tmp"
mv -f "$tmp" "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$out"
else
	shasum -a 256 "$out"
fi
