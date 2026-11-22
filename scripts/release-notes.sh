#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# Generate the release notes from the gated server and the app's support schedule.
set -eu
today=$(date -u +%F)
jq -er --arg today "$today" --slurpfile status "$1" '
  def validdate: type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$");
  def positive_int: type == "number" and . >= 1 and . == floor;
  (if .formatVersion != 1 or (.supportSchedule | type) != "array" then
    error("unknown compat.json format")
  elif any(.supportSchedule[];
    (.contractMajor | positive_int | not)
    or (.endDate != null and (.endDate | validdate | not))
    or (.upcomingLowestMinor != null and
      ((.upcomingLowestMinor.minor | positive_int | not) or (.upcomingLowestMinor.date | validdate | not)))) then
    error("invalid contract support schedule")
  else .supportSchedule end) as $schedule |
  ($status[0].contracts | map(.version) | unique) as $versions |
  if ($versions | length) == 0 or any($versions[]; type != "string" or (test("^[1-9][0-9]*\\.[0-9]+$") | not))
    then error("candidate status has no valid contract versions") else empty end,
  "## Contract versions served", "",
  ($versions[] | "- Contract \(.)"),
  "", "## Upcoming app support dates", "",
  ([
    $schedule[] as $entry |
    (select($entry.endDate != null and $entry.endDate > $today) |
      "- \($entry.endDate) UTC — App support for contract major \($entry.contractMajor) ends."),
    (select($entry.upcomingLowestMinor != null and $entry.upcomingLowestMinor.date > $today) |
      "- \($entry.upcomingLowestMinor.date) UTC — Minimum accepted contract for major \($entry.contractMajor) becomes \($entry.contractMajor).\($entry.upcomingLowestMinor.minor).")
  ] | sort | if length == 0 then "No upcoming dates are scheduled." else .[] end)
' "$2"
