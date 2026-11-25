#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
set -eu
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
archive="$work/source.tar"
version=1.2.3
digest="sha256:$(printf '%064d' 0)"
# Archive the checked out commit like the release workflow.
git archive -o "$archive" HEAD
mkdir "$work/one" "$work/two"
for dir in "$work/one" "$work/two"; do
    sh scripts/stamp-install.sh "$version" "$digest" "$dir/install.sh" >/dev/null
    python3 scripts/stage-operator.py "$archive" "$version" "$dir"
done
name="abyssreader-operator-$version"
cmp "$work/one/$name.zip" "$work/two/$name.zip"
python3 -m zipfile -e "$work/one/$name.zip" "$work"
diff -r "$work/$name/.agents/skills" "$work/$name/.claude/skills"
cd "$work/$name"
test -f docs/guides/README.md
test -f docs/reference/cli.md
test -f docs/developers/README.md
test -f LICENSE
test -f CONTRIBUTING.md
test -f .agents/skills/customize-kit/SKILL.md
cmp "$work/one/install.sh" install.sh
# Perform the same hook operation from the unpacked workspace.
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ssh host cat /opt/abyssreader-server/.env"}}' |
    python3 .claude/hooks/keep-secrets-on-server.py | grep -q '"permissionDecision": "deny"'
