#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# Build a real second server release, with an additional embedded Goose migration.
set -eu
cd "$(dirname "$0")/../.."
out=$1
mkdir -p "$out"
out=$(cd "$out" && pwd)
src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT
cp -R cmd internal schemas contract contract.go go.mod go.sum "$src/"
cp scripts/testdata/99999_test_upgrade.sql "$src/internal/store/migrations/"
CGO_ENABLED=0 GOOS=linux go build -o "$out/test-broken-upgrade" ./scripts/testdata/broken-upgrade
(cd "$src" && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags '-s -w -X main.version=0.0.1-rc.1' -o "$out/test-good-upgrade" ./cmd/abyssreader-server)
