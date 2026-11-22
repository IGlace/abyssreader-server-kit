#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
set -eu
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '%s\n' '{"contracts":[{"version":"2.1"},{"version":"1.4"}]}' >"$tmp/status.json"
cat >"$tmp/compat.json" <<'JSON'
{
  "formatVersion": 1,
  "supportSchedule": [
    {"contractMajor": 1, "endDate": "2099-08-01", "upcomingLowestMinor": {"minor": 5, "date": "2099-01-01"}},
    {"contractMajor": 2, "endDate": null, "upcomingLowestMinor": null},
    {"contractMajor": 3, "endDate": "1999-01-01", "upcomingLowestMinor": null},
    {"contractMajor": 4, "endDate": "2099-03-01"}
  ]
}
JSON
sh scripts/release-notes.sh "$tmp/status.json" "$tmp/compat.json" >"$tmp/notes"
grep -Fxq -- '- Contract 1.4' "$tmp/notes"
grep -Fxq -- '- Contract 2.1' "$tmp/notes"
grep -Fxq -- '- 2099-01-01 UTC — Minimum accepted contract for major 1 becomes 1.5.' "$tmp/notes"
grep -Fxq -- '- 2099-03-01 UTC — App support for contract major 4 ends.' "$tmp/notes"
grep -Fxq -- '- 2099-08-01 UTC — App support for contract major 1 ends.' "$tmp/notes"
if grep -q '1999' "$tmp/notes"; then exit 1; fi
printf '%s\n' '{"formatVersion":1,"supportSchedule":[]}' >"$tmp/compat.json"
sh scripts/release-notes.sh "$tmp/status.json" "$tmp/compat.json" | grep -Fxq 'No upcoming dates are scheduled.'
printf '%s\n' '{"formatVersion":2,"supportSchedule":[]}' >"$tmp/compat.json"
if sh scripts/release-notes.sh "$tmp/status.json" "$tmp/compat.json" >/dev/null 2>&1; then exit 1; fi
