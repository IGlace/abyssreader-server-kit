#!/usr/bin/env sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
#
# Every tracked file carries its licence in its first lines, except the licence
# texts themselves, generated code, go.sum, contract/ (covered by its own validator), the schema fixture and package locks
# used by that validator and the example images under examples/, which cannot carry a
# header (the README of their folder states their licence).
set -eu
missing=0
for f in $(git ls-files); do
  case "$f" in
    LICENSE|LICENSE-DOCS|NOTICE|go.sum|contract/*|internal/store/sqlcdb/*) continue ;;
    scripts/contract/package*.json|scripts/contract/requirements*.txt|scripts/contract/requirements.in|scripts/contract/source-definition.schema.json) continue ;;
    examples/*.png|examples/*.jpg|examples/*.webp) continue ;;
  esac
  if ! head -n 6 "$f" | grep -q 'SPDX-License-Identifier: \(PolyForm-Noncommercial-1.0.0\|CC-BY-NC-SA-4.0\)'; then
    echo "missing licence header: $f"
    missing=1
  fi
  if ! head -n 6 "$f" | grep -q 'Copyright 2026 Abyss Reader'; then
    echo "missing copyright line: $f"
    missing=1
  fi
done
[ "$missing" -eq 0 ] && echo "every file carries its licence header"
exit "$missing"
