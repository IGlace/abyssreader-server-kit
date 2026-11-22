#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# shellcheck disable=SC2329
set -eu
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sed '/^COMMAND=install$/,$d' install.sh >"$work/functions.sh"
(
	# shellcheck disable=SC1091
	. "$work/functions.sh"
	trap - EXIT
	installed_image() { echo "$KIT_IMAGE_REPO:1.0.0"; }
	curl() { return 22; }
	if (upgrade_image 1.2.3) >"$work/upgrade" 2>&1; then exit 1; fi
	if (release_check) >"$work/check" 2>&1; then exit 1; fi
	grep -Fq 'kit repository must be public' "$work/upgrade"
	grep -Fq 'image-digest.txt' "$work/upgrade"
	grep -Fq 'kit repository must be public' "$work/check"
)
