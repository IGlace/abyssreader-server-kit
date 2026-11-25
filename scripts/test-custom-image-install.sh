#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
set -eu
umask 077
cd "$(dirname "$0")/.."
root=$PWD
work=$(mktemp -d "$root/dist/custom-install.XXXXXXXX")
image="abyssreader-kit-local:test-$$"
export COMPOSE_PROJECT_NAME="kit-custom-$$"
export ABYSS_INSTALL_DIR="$work/install" ABYSS_INSTALL_TEST=1 ABYSS_INSTALL_WAIT=30
cleanup() {
    if [ -f "$ABYSS_INSTALL_DIR/compose.yaml" ]; then
        (cd "$ABYSS_INSTALL_DIR" && docker compose down -v) >/dev/null 2>&1 || true
    fi
    docker image rm "$image" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT
docker tag "${1:?usage: test-custom-image-install.sh BUILT_IMAGE [PORT]}" "$image"
cat >"$work/.env" <<ENV
KIT_IMAGE=$image
ABYSS_SERVER_NAME=Custom image smoke test
ABYSS_OPERATOR_NAME=Test operator
ABYSS_OPERATOR_CONTACT=mailto:operator@example.org
ABYSS_PUBLIC_URL=https://custom.example.org
ABYSS_INSTALL_HTTPS=own-proxy
ABYSS_INSTALL_PORT=${2:-18876}
ABYSS_ACCESS_KEY=
ENV
sh install.sh --answers "$work/.env"
(
    cd "$ABYSS_INSTALL_DIR"
    test "$(docker compose config --images server)" = "$image"
    docker compose exec -T server abyssreader-server status | grep -q 'Custom image smoke test'
)
sh install.sh configure --answers "$work/.env"
