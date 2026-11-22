#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
#
# Run after installing a server with an access key and a DSN. Change both and confirm
# their rollback copy disappears only once the new configuration has passed startup.
set -eu
umask 077
installer=$1
dir=$2
work=$3

access_key() { sed -n 's/^ABYSS_ACCESS_KEY="\(.*\)"$/\1/p' "$dir/.env"; }
before=$(access_key)
[ -n "$before" ] || { echo "test needs an existing access key" >&2; exit 1; }
grep -Eq '^ABYSS_ERROR_REPORTING_DSN="https://' "$dir/.env"
printf 'ABYSS_ACCESS_KEY=new\nABYSS_ERROR_REPORTING_DSN=\n' >"$work/rotate.env"
sh "$installer" configure --answers "$work/rotate.env" </dev/null >"$work/rotate.log" 2>&1 || {
	echo "configure failed to rotate secrets" >&2
	exit 1
}
after=$(access_key)
[ -n "$after" ] && [ "$after" != "$before" ] || { echo "configure did not rotate the access key" >&2; exit 1; }
grep -qx 'ABYSS_ERROR_REPORTING_DSN=""' "$dir/.env"
if [ -e "$dir/.installer/previous" ]; then
	echo "configure retained the rollback copy after a successful start" >&2
	exit 1
fi
