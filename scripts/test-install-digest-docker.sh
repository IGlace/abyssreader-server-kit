#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# Exercise tag@digest through a real Docker daemon and a disposable loopback registry.
set -eu
umask 077
cd "$(dirname "$0")/.."
base=${1:?usage: scripts/test-install-digest-docker.sh LOCAL_IMAGE}
docker image inspect "$base" >/dev/null
work=$(mktemp -d)
registry="kit-digest-registry-$$"
registry_id=""
registry_image=registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
port=${TEST_DIGEST_PORT:-18082}
host_os=$(uname -s)
export COMPOSE_PROJECT_NAME="kit-digest-$$"
export DIGEST_TEST_DIR="$work/install"
DIGEST_TEST_MODE=""
if [ "$host_os" != Linux ] || [ "$(id -u)" != 0 ]; then DIGEST_TEST_MODE=1; fi
export DIGEST_TEST_MODE
old_version=1.0.0
new_version=1.0.1
repo=""
old_digest=""
new_digest=""

compose() { (cd "$DIGEST_TEST_DIR" && docker compose "$@"); }
cleanup() {
	status=$?
	trap - EXIT
	if [ -f "$DIGEST_TEST_DIR/compose.yaml" ]; then
		if [ "$status" -ne 0 ]; then compose logs --tail 20 || true; fi
		compose down -v >/dev/null 2>&1 || true
	fi
	if [ -n "$repo" ]; then
		for version in "$old_version" "$new_version"; do
			docker image rm "$repo:$version" >/dev/null 2>&1 || true
		done
		for digest in "$old_digest" "$new_digest"; do
			if [ -n "$digest" ]; then docker image rm "$repo@$digest" >/dev/null 2>&1 || true; fi
		done
	fi
	if [ -n "$registry_id" ]; then docker rm -fv "$registry_id" >/dev/null 2>&1 || true; fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

if [ "$host_os" = Darwin ]; then
	# Desktop maps published ports onto macOS. Bind in the daemon VM for this fixture.
	endpoint=127.0.0.1:${TEST_DIGEST_REGISTRY_PORT:-18500}
	registry_id=$(docker run -d --name "$registry" --network host -e "REGISTRY_HTTP_ADDR=$endpoint" "$registry_image")
else
	registry_id=$(docker run -d --name "$registry" -p 127.0.0.1::5000 "$registry_image")
	endpoint=$(docker port "$registry_id" 5000/tcp)
fi
registry_ready() {
	if [ "$host_os" = Darwin ]; then
		docker exec "$registry_id" wget -qO /dev/null -T 2 "http://$endpoint/v2/"
	else
		curl -fsS --max-time 2 "http://$endpoint/v2/"
	fi
}
for attempt in $(seq 1 20); do
	if registry_ready >/dev/null 2>&1; then break; fi
	if [ "$attempt" = 20 ]; then docker logs "$registry_id"; exit 1; fi
	sleep 1
done
repo="$endpoint/abyssreader-server-kit"
docker tag "$base" "$repo:$old_version"
# A label gives the candidate a distinct config and digest without changing its schema.
printf 'FROM %s\nLABEL org.opencontainers.image.version=%s\n' "$base" "$new_version" |
	docker build -q -t "$repo:$new_version" -f - "$work" >/dev/null

push_fixture() {
	docker push "$repo:$1" >"$work/push.log"
	awk '$2 == "digest:" {print $3}' "$work/push.log"
}
old_digest=$(push_fixture "$old_version")
new_digest=$(push_fixture "$new_version")
[ "$old_digest" != "$new_digest" ]
old_ref="$repo:$old_version@$old_digest"
new_ref="$repo:$new_version@$new_digest"

make_installer() {
	sh scripts/stamp-install.sh "$1" "$2" "$work/stamped.sh" >/dev/null
	# Keep the release code and change only the scratch install directory and registry.
	# shellcheck disable=SC2016
	sed -e "s|^KIT_IMAGE_REPO=.*|KIT_IMAGE_REPO=$repo|" \
		-e 's|^INSTALL_DIR=.*|INSTALL_DIR=$DIGEST_TEST_DIR|' \
		-e 's|^TEST_MODE=.*|TEST_MODE=$DIGEST_TEST_MODE|' \
		"$work/stamped.sh" >"$work/install-$1.sh"
}
make_installer "$old_version" "$old_digest"
make_installer "$new_version" "$new_digest"
docker image rm "$repo:$old_version" "$repo:$new_version" >/dev/null
for ref in "$old_ref" "$new_ref"; do
	if docker image inspect "$ref" >/dev/null 2>&1; then
		echo "Fixture digest was cached before install: $ref" >&2
		exit 1
	fi
done

cat >"$work/answers.env" <<EOF_ANSWERS
ABYSS_PUBLIC_URL=https://kit.example.org
ABYSS_INSTALL_HTTPS=own-proxy
ABYSS_INSTALL_PORT=$port
ABYSS_SERVER_NAME=Digest test kit
ABYSS_OPERATOR_NAME=CI
ABYSS_OPERATOR_CONTACT=mailto:ci@example.org
EOF_ANSWERS

check_image() {
	compose config --images server | grep -Fxq "$1"
	docker inspect -f '{{.Config.Image}}' "$(compose ps -q server)" | grep -Fxq "$1"
	docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$1" | grep -Fxq "$repo@${1##*@}"
	curl -fsS --max-time 5 "http://127.0.0.1:$port/api/status" |
		jq -e '.name == "Digest test kit"' >/dev/null
}

sh "$work/install-$old_version.sh" --answers "$work/answers.env" </dev/null
check_image "$old_ref"
# The checker mocks GitHub release metadata only. Docker and Compose still
# report the installed image to the real installer.
sh scripts/testdata/release-check.sh "$work/install-$old_version.sh" "$work" "$old_version"
sh "$work/install-$new_version.sh" upgrade </dev/null
check_image "$new_ref"

# Force rollback to pull the old digest instead of reusing an already cached copy.
docker image rm "$repo@$old_digest" >/dev/null
if docker image inspect "$old_ref" >/dev/null 2>&1; then
	echo "Fixture digest was cached before rollback: $old_ref" >&2
	exit 1
fi
printf '%s\n' ROLLBACK | sh "$work/install-$new_version.sh" rollback
check_image "$old_ref"
echo 'Digest-pinned install, upgrade check, upgrade and rollback passed.'
