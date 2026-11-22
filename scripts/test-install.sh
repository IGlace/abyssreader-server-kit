#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
#
# The installer end to end, with real Docker, in the own-proxy mode: a fresh install from
# an answers file, a second run, a configure, a configure that cannot start (it goes back by
# itself), a configure with bad answers, a hand-edited compose.yaml, and the Caddy mode's files
# checked by `docker compose config`. It asserts the files written, their modes and owners,
# /api/status, the log rotation, the loopback-only port, and that no secret is printed.
#
# CI runs it on a throwaway GitHub runner as root, against /opt/abyssreader-server, with the
# release copy (no test hooks):
#   docker build --build-arg VERSION=0.0.0-ci -t ghcr.io/iglace/abyssreader-server-kit:0.0.0-ci .
#   scripts/stamp-install.sh --local-tag 0.0.0-ci
#   sudo sh scripts/test-install.sh 0.0.0-ci
# On a development machine (not as root, not Linux), in a throwaway folder, with a copy that
# keeps the test hooks (use a Compose project name of your own on a shared machine):
#   scripts/stamp-install.sh --keep-test-hooks 0.0.0-ci
#   COMPOSE_PROJECT_NAME=kit-install-test ABYSS_INSTALL_TEST=1 sh scripts/test-install.sh 0.0.0-ci
# It removes what it installed (containers, volumes, the folder) at the end.
set -eu
umask 077
cd "$(dirname "$0")/.."
VERSION=${1:?usage: scripts/test-install.sh VERSION}
IMAGE="ghcr.io/iglace/abyssreader-server-kit:$VERSION"
SCRIPT="$PWD/dist/install.sh"
PORT=${TEST_PORT:-18080}
BUSY_PORT=${TEST_BUSY_PORT:-18081}

if [ -n "${ABYSS_INSTALL_TEST:-}" ]; then
	DIR=$(mktemp -d)/abyssreader-server
	export ABYSS_INSTALL_DIR="$DIR"
	CHECK_OWNER=""
else
	DIR=/opt/abyssreader-server
	CHECK_OWNER=1
	[ "$(id -u)" = 0 ] || {
		echo "run as root (sudo), or with ABYSS_INSTALL_TEST=1 on a development machine" >&2
		exit 2
	}
	[ ! -e "$DIR" ] || {
		echo "$DIR exists: this test needs a machine without an installed server" >&2
		exit 2
	}
fi
ABYSS_INSTALL_WAIT="${ABYSS_INSTALL_WAIT:-90}"
export ABYSS_INSTALL_WAIT
T=$(mktemp -d)
BUSY_PID=""

grep -q "^KIT_VERSION=$VERSION\$" "$SCRIPT" || {
	echo "$SCRIPT is not stamped with $VERSION (scripts/stamp-install.sh --local-tag $VERSION)" >&2
	exit 2
}
if [ -n "${ABYSS_INSTALL_TEST:-}" ]; then
	grep -q 'ABYSS_INSTALL_TEST' "$SCRIPT" || {
		echo "$SCRIPT has no test hooks: stamp it with --keep-test-hooks for a run without root" >&2
		exit 2
	}
elif grep -q 'ABYSS_INSTALL_\(DIR\|WAIT\|TEST\)' "$SCRIPT"; then
	echo "FAIL: the release copy $SCRIPT still reads the test hooks" >&2
	exit 1
fi
docker image inspect "$IMAGE" >/dev/null

finish() {
	status=$?
	if [ -n "$BUSY_PID" ]; then kill "$BUSY_PID" 2>/dev/null || true; fi
	if [ -f "$DIR/compose.yaml" ]; then
		if [ "$status" != 0 ]; then (cd "$DIR" && docker compose logs --tail 50) || true; fi
		(cd "$DIR" && docker compose down -v >/dev/null 2>&1) || true
	fi
	rm -rf "$DIR" "$T"
	if [ "$status" = 0 ]; then echo "install.sh end to end: all checks passed"; else echo "install.sh end to end: FAILED" >&2; fi
}
trap finish EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}
step() { printf '\n== %s\n' "$*"; }

mode_of() {
	if stat -c %a "$1" >/dev/null 2>&1; then stat -c %a "$1"; else stat -f %Lp "$1"; fi
}
owner_of() { stat -c %U:%G "$1"; }
sum_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1; else shasum -a 256 "$1" | cut -d ' ' -f 1; fi
}
expect_mode() {
	[ "$(mode_of "$1")" = "$2" ] || fail "$1 has mode $(mode_of "$1"), want $2"
	if [ -n "$CHECK_OWNER" ]; then
		[ "$(owner_of "$1")" = root:root ] || fail "$1 is owned by $(owner_of "$1"), want root:root"
	fi
}
check_files() {
	expect_mode "$DIR" 755
	expect_mode "$DIR/.env" 600
	expect_mode "$DIR/compose.yaml" 644
	expect_mode "$DIR/import" 755
	expect_mode "$DIR/.installer" 700
	expect_mode "$DIR/.installer/install.info" 600
	[ ! -e "$DIR/Caddyfile" ] || fail "a Caddyfile in the own-proxy mode"
	for f in "$DIR"/.installer/*; do
		case "$f" in */setup.*) fail "the setup's work folder $f was left behind" ;; esac
	done
}
env_value() { sed -n "s/^$1=\"\\(.*\\)\"\$/\\1/p" "$DIR/.env"; }
status_json() { curl -fsS "http://127.0.0.1:$1/api/status"; }
# no_secret LOG: the access key and the DSN key never appear in what install.sh printed.
no_secret() {
	key=$(env_value ABYSS_ACCESS_KEY)
	[ -n "$key" ] || fail "no access key in .env"
	if grep -F -q -- "$key" "$1"; then fail "install.sh printed the access key"; fi
	if grep -F -q -- "secretdsnkey" "$1"; then fail "install.sh printed the DSN"; fi
}

NAME="Tom's \"Kit\" \$HOME \\ shelf"
cat >"$T/answers.env" <<EOF
# A scripted install: the own-proxy mode on 127.0.0.1:$PORT.
ABYSS_PUBLIC_URL=https://kit.example.org
ABYSS_INSTALL_HTTPS=own-proxy
ABYSS_INSTALL_PORT=$PORT
ABYSS_SERVER_NAME='Tom\\'s "Kit" \$HOME \\ shelf'
ABYSS_OPERATOR_NAME=CI operator
ABYSS_OPERATOR_CONTACT=mailto:ci@example.org
ABYSS_SIGN_UP_OPEN=yes
ABYSS_ACCESS_KEY=generate
ABYSS_FEATURES=hosted-chapters
ABYSS_ERROR_REPORTING_DSN=https://secretdsnkey@glitchtip.invalid/1
ABYSS_BACKUP_SCHEDULE=weekly
ABYSS_BACKUP_KEEP=12
EOF
chmod 600 "$T/answers.env"

step "a Docker Compose older than the minimum is refused before anything is written"
mkdir "$T/old-compose"
cat >"$T/old-compose/docker" <<'STUB'
#!/bin/sh
case "$*" in
info) exit 0 ;;
"compose version --short") echo 2.20.1 ;;
*)
	echo "unexpected docker $*" >&2
	exit 99
	;;
esac
STUB
chmod 755 "$T/old-compose/docker"
if PATH="$T/old-compose:$PATH" sh "$SCRIPT" --answers "$T/answers.env" </dev/null >"$T/old.log" 2>&1; then fail "an old Compose was taken"; fi
grep -q "Docker Compose 2.20.1 is too old: this kit needs 2.24.0 or later" "$T/old.log" || fail "old Compose: $(cat "$T/old.log")"
if grep -q "unexpected docker" "$T/old.log"; then fail "install.sh went on after the version check: $(cat "$T/old.log")"; fi
[ ! -e "$DIR" ] || fail "an old Compose left $DIR behind"

step "fresh install from an answers file"
sh "$SCRIPT" --answers "$T/answers.env" </dev/null >"$T/install.log" 2>&1 || {
	cat "$T/install.log"
	fail "install.sh failed"
}
cat "$T/install.log"
no_secret "$T/install.log"
check_files
grep -q "^    image: $IMAGE\$" "$DIR/compose.yaml" || fail "compose.yaml does not pin $IMAGE"
grep -q "\"127.0.0.1:$PORT:8080\"" "$DIR/compose.yaml" || fail "compose.yaml does not publish 127.0.0.1:$PORT"
status_json "$PORT" >"$T/status.json"
jq -e --arg name "$NAME" '.name == $name and .accessKeyRequired == true and .signUpOpen == true and .contracts[0].features == ["hosted-chapters"]' "$T/status.json" >/dev/null ||
	fail "/api/status: $(cat "$T/status.json")"
[ "$(env_value ABYSS_ACCESS_KEY | wc -c)" -eq 44 ] || fail "the generated access key is not 43 characters"
container=$(cd "$DIR" && docker compose ps -q server)
docker inspect -f '{{.HostConfig.LogConfig.Type}} {{index .HostConfig.LogConfig.Config "max-size"}} {{index .HostConfig.LogConfig.Config "max-file"}}' "$container" | grep -qx 'json-file 10m 3' ||
	fail "log rotation: $(docker inspect -f '{{json .HostConfig.LogConfig}}' "$container")"
docker port "$container" 8080 | grep -qv '^127\.0\.0\.1:' && fail "the port is published beyond 127.0.0.1: $(docker port "$container" 8080)"
docker inspect -f '{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}}' "$container" | grep -q '^true \[ALL\]$' || fail "the server container is not read-only with no capabilities"
KEY_BEFORE=$(env_value ABYSS_ACCESS_KEY)
ENV_SUM=$(sum_of "$DIR/.env")
COMPOSE_SUM=$(sum_of "$DIR/compose.yaml")

step "a second install.sh run changes nothing"
sh "$SCRIPT" --answers "$T/answers.env" </dev/null >"$T/again.log" 2>&1 || {
	cat "$T/again.log"
	fail "the second run failed"
}
grep -q "already installed" "$T/again.log" || fail "the second run did not say the server is installed"
[ "$(sum_of "$DIR/.env")" = "$ENV_SUM" ] || fail "the second run changed .env"
[ "$(sum_of "$DIR/compose.yaml")" = "$COMPOSE_SUM" ] || fail "the second run changed compose.yaml"
check_files

step "a lock left by a stopped install.sh is taken over; a live one is not"
sh -c 'exit 0' &
dead=$!
wait "$dead" || true
mkdir "$DIR/.installer/lock" "$DIR/.installer/setup.$dead"
echo "$dead" >"$DIR/.installer/lock/pid"
echo "a copy of the secrets" >"$DIR/.installer/setup.$dead/current.env"
sh "$SCRIPT" --answers "$T/answers.env" </dev/null >"$T/stale.log" 2>&1 || fail "a stale lock stopped install.sh: $(cat "$T/stale.log")"
grep -q "Taking over the lock of an install.sh that stopped (process $dead is gone)" "$T/stale.log" || fail "no take-over message: $(cat "$T/stale.log")"
[ ! -e "$DIR/.installer/lock" ] || fail "the lock is left after the run"
[ ! -e "$DIR/.installer/setup.$dead" ] || fail "the stopped run's work folder (a copy of .env) is left"
mkdir "$DIR/.installer/lock"
echo "$$" >"$DIR/.installer/lock/pid"
if sh "$SCRIPT" --answers "$T/answers.env" </dev/null >"$T/live.log" 2>&1; then fail "a live lock was taken over"; fi
grep -q "another install.sh (process $$) is running" "$T/live.log" || fail "live lock: $(cat "$T/live.log")"
rm -rf "$DIR/.installer/lock"

step "an answers file others can read is refused when it holds a secret"
printf 'ABYSS_ACCESS_KEY=an-access-key-of-24-chars\n' >"$T/readable.env"
chmod 644 "$T/readable.env"
if sh "$SCRIPT" configure --answers "$T/readable.env" </dev/null >"$T/readable.log" 2>&1; then fail "a readable secret answers file was taken"; fi
grep -q "holds a secret" "$T/readable.log" || fail "readable: $(cat "$T/readable.log")"
if grep -q "an-access-key-of-24-chars" "$T/readable.log"; then fail "the refusal printed the key"; fi
[ "$(sum_of "$DIR/.env")" = "$ENV_SUM" ] || fail "the refused answers file changed .env"

step "configure changes a setting and keeps the secrets"
printf 'ABYSS_SERVER_NAME=Kit renamed\nABYSS_SIGN_UP_OPEN=no\nABYSS_ACCESS_KEY=generate\n' >"$T/rename.env"
sh "$SCRIPT" configure --answers "$T/rename.env" </dev/null >"$T/configure.log" 2>&1 || {
	cat "$T/configure.log"
	fail "configure failed"
}
no_secret "$T/configure.log"
check_files
status_json "$PORT" | jq -e '.name == "Kit renamed" and .signUpOpen == false and .accessKeyRequired == true' >/dev/null || fail "the new settings are not served"
[ "$(env_value ABYSS_ACCESS_KEY)" = "$KEY_BEFORE" ] || fail "configure changed the access key"
grep -q '^ABYSS_ERROR_REPORTING_DSN="https://secretdsnkey@glitchtip.invalid/1"$' "$DIR/.env" || fail "configure lost the DSN"
ENV_SUM=$(sum_of "$DIR/.env")

step "configure with bad answers changes nothing"
printf 'ABYSS_PUBLIC_URL=http://kit.example.org\n' >"$T/bad.env"
if sh "$SCRIPT" configure --answers "$T/bad.env" </dev/null >"$T/bad.log" 2>&1; then fail "bad answers were taken"; fi
grep -q "ABYSS_PUBLIC_URL" "$T/bad.log" || fail "the refusal does not name ABYSS_PUBLIC_URL: $(cat "$T/bad.log")"
[ "$(sum_of "$DIR/.env")" = "$ENV_SUM" ] || fail "bad answers changed .env"
check_files
status_json "$PORT" >/dev/null || fail "the server stopped after bad answers"

step "a configure that cannot start goes back to the previous settings"
python3 -m http.server --bind 127.0.0.1 "$BUSY_PORT" >/dev/null 2>&1 &
BUSY_PID=$!
sleep 1
printf 'ABYSS_INSTALL_PORT=%s\nABYSS_SERVER_NAME=Never served\n' "$BUSY_PORT" >"$T/busy.env"
if sh "$SCRIPT" configure --answers "$T/busy.env" </dev/null >"$T/busy.log" 2>&1; then
	cat "$T/busy.log"
	fail "a configure on a taken port succeeded"
fi
cat "$T/busy.log"
grep -q "previous ones again" "$T/busy.log" || fail "configure did not say it went back"
grep -q "docker compose logs" "$T/busy.log" || fail "configure did not say how to read the logs"
[ "$(sum_of "$DIR/.env")" = "$ENV_SUM" ] || fail "the failed configure left the new .env"
[ "$(sum_of "$DIR/compose.yaml")" = "$COMPOSE_SUM" ] || fail "the failed configure left the new compose.yaml"
check_files
status_json "$PORT" | jq -e '.name == "Kit renamed"' >/dev/null || fail "the previous settings are not served after the rollback"
kill "$BUSY_PID"
BUSY_PID=""

step "an edited compose.yaml is kept unless the operator says yes"
printf '# my own line\n' >>"$DIR/compose.yaml"
EDITED_SUM=$(sum_of "$DIR/compose.yaml")
printf 'ABYSS_SERVER_NAME=Kit edited\n' >"$T/edit.env"
sh "$SCRIPT" configure --answers "$T/edit.env" </dev/null >"$T/edit.log" 2>&1 || {
	cat "$T/edit.log"
	fail "configure with an edited compose.yaml failed"
}
grep -q "was changed since install.sh wrote it" "$T/edit.log" || fail "configure did not notice the edited compose.yaml"
[ "$(sum_of "$DIR/compose.yaml")" = "$EDITED_SUM" ] || fail "configure replaced an edited compose.yaml without asking"
status_json "$PORT" | jq -e '.name == "Kit edited"' >/dev/null || fail "configure with a kept compose.yaml did not apply the name"
check_files

step "a configure stopped (SIGTERM) while the new settings start goes back to the previous ones"
ENV_SUM=$(sum_of "$DIR/.env")
printf 'ABYSS_SERVER_NAME=Kit interrupted\n' >"$T/term.env"
sh "$SCRIPT" configure --answers "$T/term.env" </dev/null >"$T/term.log" 2>&1 &
pid=$!
tries=0
until grep -q "Starting the server" "$T/term.log" 2>/dev/null || [ "$tries" -ge 600 ]; do
	sleep 0.1
	tries=$((tries + 1))
done
kill -TERM "$pid" 2>/dev/null || true
code=0
wait "$pid" || code=$?
cat "$T/term.log"
if [ "$code" = 0 ]; then
	echo "SKIP: the configure finished before the signal arrived"
else
	[ "$code" = 143 ] || fail "SIGTERM exit code $code, want 143"
	grep -q "going back to the previous ones" "$T/term.log" || fail "no rollback message"
	[ "$(sum_of "$DIR/.env")" = "$ENV_SUM" ] || fail "the interrupted configure left the new .env"
	status_json "$PORT" | jq -e '.name == "Kit edited"' >/dev/null || fail "the previous settings are not served after the interrupt"
	check_files
	if docker ps -aq --filter 'name=^abyssreader-setup-' | grep -q .; then fail "a setup container is left"; fi
fi

step "successful configure removes the old access key and DSN from the rollback folder"
sh scripts/testdata/configure-secrets.sh "$SCRIPT" "$DIR" "$T"

step "the Caddy mode's files pass docker compose config"
W="$T/caddy"
mkdir -m 700 "$W"
cat >"$W/answers.env" <<'EOF'
ABYSS_PUBLIC_URL=https://reader.example.org
ABYSS_INSTALL_HTTPS=caddy
ABYSS_INSTALL_DNS_CHECK=skip
ABYSS_SERVER_NAME=Caddy check
ABYSS_OPERATOR_NAME=CI
ABYSS_OPERATOR_CONTACT=mailto:ci@example.org
EOF
docker run --rm -i --user 0:0 --cap-drop ALL --security-opt no-new-privileges:true --read-only --tmpfs /tmp \
	-v "$W:/work" "$IMAGE" setup --work /work --image "$IMAGE" </dev/null
(cd "$W/out" && docker compose -f compose.yaml config -q) || fail "the Caddy mode's compose.yaml is not valid"
grep -q '"443:443"' "$W/out/compose.yaml" || fail "Caddy does not publish 443"
grep -q 'reader.example.org {' "$W/out/Caddyfile" || fail "the Caddyfile does not name the address"
if grep -q '8080:8080' "$W/out/compose.yaml"; then fail "the Caddy mode publishes the server's port"; fi

step "server backups, recovery from a failed migration, rollback, restore and uninstall"
sh scripts/test-backup-install.sh "$DIR" "$T" "$SCRIPT" "$IMAGE" "$PORT"
