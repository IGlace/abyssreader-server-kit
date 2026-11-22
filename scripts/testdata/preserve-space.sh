#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# The caller supplies a private container below reserve with valid SQLite data.
set -eu
container=$1
version=$2
work=$3
docker exec "$container" abyssreader-server admin backup list >"$work/space-list-before"
if docker exec "$container" abyssreader-server maintenance snapshot-preserve 1 "$version" >"$work/space-preserve.log" 2>&1; then
	echo "snapshot-preserve accepted a full volume" >&2
	exit 1
fi
grep -qi 'insufficient free space' "$work/space-preserve.log"
if grep -Fqi 'raw, unverified' "$work/space-preserve.log"; then exit 1; fi
docker exec "$container" abyssreader-server admin backup list >"$work/space-list-after"
cmp "$work/space-list-before" "$work/space-list-after"
