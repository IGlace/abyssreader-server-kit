#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# Runs after test-install.sh has installed an isolated stack; its caller cleans up.
set -eu
umask 077
DIR=$1
T=$2
SCRIPT=$3
IMAGE=$4
PORT=$5
repo=${IMAGE%:*}
good_tag=0.0.1-rc.1
broken_tag="0.0.2-rc.$$"
good="$repo:$good_tag"
broken="$repo:$broken_tag"
compose() { (cd "$DIR" && docker compose "$@"); }
admin() { compose exec -T server abyssreader-server admin "$@"; }
healthy() { curl -fsS "http://127.0.0.1:$PORT/api/status" >/dev/null; }
save() {
	generation=$(admin backup now)
	admin backup export "$generation" >"$T/$1.tar"
}
# Compare the schema and every table in the two SQLite snapshots, plus image contents.
compare() {
	python3 - "$T/$1.tar" "$T/$2.tar" "$T" <<'PY'
import os, sqlite3, sys, tarfile
def contents(archive, n):
    with tarfile.open(archive) as t:
        path = os.path.join(sys.argv[3], str(n)+".db")
        with open(path,"wb") as f:
            f.write(t.extractfile("abyssreader.db").read())
        with sqlite3.connect(path) as db:
            dump = list(db.iterdump())
        images = {m.name: t.extractfile(m).read() for m in t if m.name.startswith(("covers/", "pages/"))}
        assert images, "expected stored images"
        return dump, images
assert contents(sys.argv[1],1) == contents(sys.argv[2],2), "restored schema, rows or images differ"
PY
}
fail() {
	echo "FAIL: $*" >&2
	exit 1
}
base_schema=$(compose exec -T server abyssreader-server maintenance schema-version)
case "$base_schema" in
	''|*[!0-9]*) fail "invalid base image schema: $base_schema" ;;
esac
expect_failure() {
	if "$@" >"$T/refusal.log" 2>&1; then fail "expected refusal from $*"; fi
}
image_is() {
	actual=$(docker inspect -f '{{.Config.Image}}' "$(compose ps -q server)")
	[ "$actual" = "$1" ] || fail "running image $actual, expected $1"
}
check_data() {
	healthy
	image_is "$1"
	save actual
	compare "$2" actual
}
if [ "$(uname -s)" = Linux ]; then
	mkdir "$DIR/.installer/lock"
	sh "$SCRIPT" recover >"$T/empty-lock.log"
	grep -q 'Taking over the lock' "$T/empty-lock.log"
fi

for tag in latest v1.0.0 01.2.3 1.2.3/other 1.2.3-rc.x; do
	expect_failure sh "$SCRIPT" upgrade "$tag"
	grep -q 'specify an exact release' "$T/refusal.log"
done

bin=dist
if [ ! -x "$bin/test-broken-upgrade" ] || [ ! -x "$bin/test-good-upgrade" ]; then
	bin="$T/bin"
	arch=$(docker version --format '{{.Server.Arch}}')
	GOARCH=$arch sh scripts/testdata/build-upgrade-fixtures.sh "$bin"
fi
cp "$bin/test-broken-upgrade" "$T/fill-test"
chmod 755 "$T/fill-test"

# A scheduled backup succeeds on a fresh volume. Fill the same tmpfs below the
# promised reserve, then prove that manual failure keeps the prior archive intact.
limited=$(docker run -d --rm --network none --tmpfs '/data:rw,size=72m,uid=65532,gid=65532,mode=0700' \
	--mount "type=bind,source=$T/fill-test,target=/fill-test,readonly" \
	-e ABYSS_SERVER_NAME=limited -e ABYSS_OPERATOR_NAME=CI -e ABYSS_OPERATOR_CONTACT=mailto:test@example.org \
	-e ABYSS_PUBLIC_URL=https://kit.example.org -e ABYSS_BACKUP_KEEP=1 "$IMAGE")
trap 'docker rm -fv "$limited" >/dev/null 2>&1 || true' EXIT
tries=0
while [ "$tries" -lt 100 ]; do
	scheduled=$(docker logs "$limited" 2>&1 | sed -n 's/.*scheduled backup complete.*generation=\([^ ]*\).*/\1/p' | head -1)
	[ -z "$scheduled" ] || break
	sleep 1
	tries=$((tries + 1))
done
[ -n "$scheduled" ] || fail "fresh server did not make a scheduled backup"
docker exec "$limited" abyssreader-server admin backup export "$scheduled" >"$T/scheduled.tar"
# Prove that the extra upgrade copy matters. The available block count sits
# strictly between reserve + 2 DBs and reserve + 3 DBs; this server has no images.
docker exec "$limited" /fill-test upgrade-headroom
docker exec "$limited" abyssreader-server maintenance preflight backup
expect_failure docker exec "$limited" abyssreader-server maintenance preflight upgrade
grep -Eiq 'space' "$T/refusal.log"
docker exec "$limited" abyssreader-server status
docker exec "$limited" /fill-test fill
sh scripts/testdata/preserve-space.sh "$limited" "${IMAGE##*:}" "$T"
expect_failure docker exec "$limited" abyssreader-server admin backup now
grep -Eiq 'insufficient free space|disk is full|no space left' "$T/refusal.log"
docker exec "$limited" abyssreader-server admin backup export "$scheduled" >"$T/kept.tar"
cmp "$T/kept.tar" "$T/scheduled.tar"
docker exec "$limited" abyssreader-server status
docker rm -fv "$limited" >/dev/null
trap - EXIT
sh "$SCRIPT" restore --yes "$T/scheduled.tar" </dev/null

# Retain the starting image digest to stand in for the remote registry, including
# pulling the previous image after the runner has genuinely evicted its local tag.
base_cache="kit-installer-registry-$$:base"
for fixture in "$good" "$broken" "$base_cache"; do
	if docker image inspect "$fixture" >/dev/null 2>&1; then fail "test image already exists: $fixture"; fi
done
TEST_BASE_DIGEST=$(docker inspect --format '{{.Id}}' "$IMAGE")
docker tag "$IMAGE" "$base_cache"
cleanup_images() {
	status=$?
	# During the lost-host simulation the outer cleanup has no compose.yaml.
	# Use the test's moved folder and the exact project captured from its server.
	if [ "$status" -ne 0 ] && [ -n "${lost_project:-}" ] && [ -f "$T/lost-installation/compose.yaml" ] && [ ! -f "$DIR/compose.yaml" ]; then
		(cd "$T/lost-installation" && docker compose -p "$lost_project" down -v >/dev/null 2>&1) || true
	fi
	if [ -n "${project:-}" ]; then
		for label in caddy_config caddy_data; do docker volume rm "$project-464-$$-$label" >/dev/null 2>&1 || true; done
	fi
	if [ "$status" -ne 0 ]; then docker tag "$TEST_BASE_DIGEST" "$IMAGE" >/dev/null 2>&1 || true; fi
	docker image rm "$good" "$broken" "$base_cache" >/dev/null 2>&1 || true
}
trap cleanup_images EXIT
for kind in good broken; do
	context="$T/image-$kind"
	mkdir "$context"
	cp "$bin/test-$kind-upgrade" "$context/server"
	if [ "$kind" = good ]; then
		printf 'FROM %s\nCOPY --chmod=0755 server /usr/local/bin/abyssreader-server\n' "$IMAGE" >"$context/Dockerfile"
		tag=$good
	else
		printf 'FROM %s\nCOPY --chmod=0755 server /broken\nENTRYPOINT ["/broken"]\nCMD ["serve"]\n' "$IMAGE" >"$context/Dockerfile"
		tag=$broken
	fi
	docker build -q -t "$tag" "$context" >/dev/null
done

TEST_WORK=$T
TEST_INSTALL_DIR=$DIR
TEST_GOOD_IMAGE=$good
TEST_BROKEN_IMAGE=$broken
TEST_BASE_IMAGE=$IMAGE
TEST_PYTHON=$(command -v python3)
TEST_DRIVER=$PWD/scripts/testdata/installer-faults.py
TEST_REAL_DOCKER=$(command -v docker)
TEST_REAL_MV=$(command -v mv)
TEST_REAL_SLEEP=$(command -v sleep)
export TEST_WORK TEST_INSTALL_DIR TEST_GOOD_IMAGE TEST_BROKEN_IMAGE TEST_BASE_IMAGE TEST_BASE_DIGEST
export TEST_PYTHON TEST_DRIVER TEST_REAL_DOCKER TEST_REAL_MV TEST_REAL_SLEEP
mkdir "$T/wrappers"
for name in docker mv sleep; do
	# Expand these variables when the wrapper runs.
	# shellcheck disable=SC2016
	printf '#!/bin/sh\nexec "$TEST_PYTHON" "$TEST_DRIVER" %s "$@"\n' "$name" >"$T/wrappers/$name"
	chmod +x "$T/wrappers/$name"
done
export PATH="$T/wrappers:$PATH"
interrupt() {
	boundary=$1
	sig=$2
	shift 2
	echo "Interrupting $1 at $boundary ($sig)"
	"$TEST_PYTHON" "$TEST_DRIVER" interrupt "$boundary" "$sig" sh "$SCRIPT" "$@"
}
stamp() {
	if [ -n "${ABYSS_INSTALL_TEST:-}" ]; then
		sh scripts/stamp-install.sh --keep-test-hooks "$@" >/dev/null
	else
		sh scripts/stamp-install.sh --local-tag "$@" >/dev/null
	fi
}
stamp "$good_tag" "$T/current-release.sh"
stamp 0.0.0 "$T/older-release.sh"
echo "Testing a failed migration at the three-copy upgrade budget"
sh scripts/testdata/limited-upgrade.sh "$T" "$IMAGE" "$broken_tag"
volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$(compose ps -q server)")
volume_fixture() {
	docker run --rm --network none --read-only --mount "type=volume,source=$volume,target=/data" \
		--mount "type=bind,source=$T/fill-test,target=/test-action,readonly" --entrypoint /test-action "$base_cache" "$@"
}
cp -R examples/manifests/sample-hosted "$DIR/import/"
chmod -R a+rX "$DIR/import/sample-hosted"
admin import /import/sample-hosted >/dev/null
volume_fixture seed-sync
save before
python3 - "$T/before.tar" "$T/identity.db" <<'PYSQL'
from pathlib import Path
import sqlite3, sys, tarfile
with tarfile.open(sys.argv[1]) as archive:
    Path(sys.argv[2]).write_bytes(archive.extractfile("abyssreader.db").read())
with sqlite3.connect(sys.argv[2]) as db:
    assert db.execute("SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='sync_records_user_title'").fetchone() == (1,)
    assert db.execute("SELECT kind FROM sync_records INDEXED BY sync_records_user_title WHERE deleted=0").fetchall() == [("favorite",)]
    assert db.execute("SELECT singleton, source_id FROM backup_hosted_identity").fetchone()[1] < 0
PYSQL
expect_failure sh "$SCRIPT" restore --yes "$T/image-broken/Dockerfile"
healthy
# Validate an invalid archive without changing the state of a stopped installation.
compose stop server
expect_failure sh "$SCRIPT" restore --yes "$T/image-broken/Dockerfile"
[ "$(docker inspect -f '{{.State.Running}}' "$(compose ps -aq server)")" = false ] || fail "invalid archive started the stopped server"
compose start server

# The broken candidate executes destructive SQL and exits before its status can pass.
expect_failure env TEST_FAST_RETRIES=1 sh "$SCRIPT" upgrade "$broken_tag"
check_data "$IMAGE" before
docker cp "$(compose ps -q server):/data/test-migration-ran" "$T/migration-ran"

cp -R examples/manifests/sample-catalog "$DIR/import/"
chmod -R a+rX "$DIR/import/sample-catalog"
admin import /import/sample-catalog >/dev/null
save changed
# The confirmation is a reading step. A plain Docker restart must still serve
# while the operator is deciding whether to replace anything.
python3 scripts/testdata/restore-prompt.py "$SCRIPT" "$DIR" "$T/before.tar" "$T/prompt.log"

# Restore a different catalog, then independently locate, export and compare
# the complete snapshot of the writes that the restore replaced.
snapshot_in_log() { sed -n 's|.*Pre-restore snapshot: /data/backups/\(kit-[^/]*\)/backup.json.*|\1|p' "$1" | tail -1; }
printf 'RESTORE\n' | sh "$SCRIPT" restore "$T/before.tar" >"$T/normal-restore.log" 2>&1
safety=$(snapshot_in_log "$T/normal-restore.log")
[ -n "$safety" ] || fail "no pre-restore generation reported"
admin backup list >"$T/list.log"
grep -F "$safety" "$T/list.log" | grep -Eiq '(^|[^[:alpha:]])complete([^[:alpha:]]|$)'
admin backup export "$safety" >"$T/prior-live.tar"
compare changed prior-live
check_data "$IMAGE" before

volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$(compose ps -q server)")
python3 scripts/testdata/check-preserved.py choose "$T/before.tar" >"$T/damage-paths"
missing_image=$(sed -n '1p' "$T/damage-paths")
corrupt_image=$(sed -n '2p' "$T/damage-paths")
damage_images() { volume_fixture damage "$missing_image" "$corrupt_image"; }
# The newest backup's cover and both live files are damaged. A still older
# verified generation supplies the cover; every row and blob must survive.
volume_fixture damage-newest "$missing_image"
damage_images
sh "$SCRIPT" restore --yes "$T/changed.tar" </dev/null >"$T/damaged-restore.log" 2>&1
safety=$(snapshot_in_log "$T/damaged-restore.log")
[ -n "$safety" ] || fail "no repaired pre-restore generation reported"
admin backup list | grep -F "$safety" | grep -Eiq '(^|[^[:alpha:]])complete([^[:alpha:]]|$)'
admin backup export "$safety" >"$T/repaired-preservation.tar"
compare before repaired-preservation
check_data "$IMAGE" changed
# The stopped live SQLite header and sidecars are torn. Preserve the exact
# bytes, then serve the verified replacement; raw material is salvage only.
compose stop server
volume_fixture truncate-db >"$T/torn.sha"
sh "$SCRIPT" restore --yes "$T/changed.tar" </dev/null >"$T/raw-restore.log" 2>&1
grep -Fqi 'raw, unverified' "$T/raw-restore.log"
raw=$(snapshot_in_log "$T/raw-restore.log")
[ -n "$raw" ] || fail "no raw pre-restore preservation reported"
admin backup list | grep -F "$raw" | grep -Fqi 'raw, unverified'
expect_failure admin backup export "$raw"
compose exec -T server abyssreader-server maintenance export-preserved "$raw" >"$T/raw.tar"
python3 scripts/testdata/check-raw.py "$T/raw.tar" "$T/torn.sha"
compose exec -T server abyssreader-server maintenance verify-preserved <"$T/raw.tar"
expect_failure sh "$SCRIPT" restore --yes "$T/raw.tar"
grep -Fqi 'raw, unverified' "$T/refusal.log"
check_data "$IMAGE" changed
expect_failure sh "$SCRIPT" restore "$T/before.tar" </dev/null
printf 'no\n' | expect_failure sh "$SCRIPT" restore "$T/before.tar"
check_data "$IMAGE" changed

# Deliver real signals after the migrated candidate has served its first status.
# Closing the PTY master also removes every usable terminal output descriptor.
for sig in INT TERM HUP; do
	interrupt candidate-probed "$sig" upgrade "$good_tag"
	check_data "$IMAGE" changed
done

# Cut the foreground process group immediately after each durable host phase.
# Recovery runs in a fresh invocation with the stale PID/lock and retained volume.
# A real candidate has already written its newer schema. Lose power again
# halfway through applying the pinned original during the first recovery.
# Temporarily remove host metadata, inspect the orphaned volume, then put the
# original journal back before asking the installer to recover it normally.
inspect_orphan_cut() {
	kind=$1
	applied=$2
	expected_pin=$(cat "$DIR/.installer/rollback-point")
	meta=$(mktemp -d "$T/probe-XXXXXXXX")
	mv "$DIR/.installer" "$meta/original"
	code=0
	sh "$SCRIPT" recover >"$meta/report" 2>&1 || code=$?
	if [ -d "$DIR/.installer" ]; then mv "$DIR/.installer" "$meta/inspected"; fi
	mv "$meta/original" "$DIR/.installer"
	[ "$code" -ne 0 ] || fail "unresolved orphan returned success"
	grep -Fqx "Operation: $kind" "$meta/report"
	grep -Fqx "Applied schema: $applied" "$meta/report"
	grep -Fqx "Pinned image supports schema: $base_schema" "$meta/report"
	grep -Fqx "Rollback pin: /data/.kit-pins/$expected_pin" "$meta/report"
	grep -Eq 'Newest complete generation: /data/backups/kit-[^/]+/backup.json' "$meta/report"
	grep -Fq "Abandoning this $kind" "$meta/report"
}
interrupt candidate-probed KILL upgrade "$good_tag"
inspect_orphan_cut upgrade 99999
interrupt mid-swap KILL recover
sh "$SCRIPT" recover
check_data "$IMAGE" changed

for phase in prepare stopping upgrade-start; do
	interrupt "phase/$phase" KILL upgrade "$good_tag"
	[ -s "$DIR/.installer/pending" ]
	sh "$SCRIPT" recover
	check_data "$IMAGE" changed
done
interrupt phase/swap KILL restore --yes "$T/before.tar"
sh "$SCRIPT" recover
check_data "$IMAGE" changed

# At this cut the live DB has already moved away. Booting the server normally
# must refuse BEFORE it can create an empty DB. Install and configure must also
# direct the operator back to recovery.
interrupt mid-swap KILL restore --yes "$T/before.tar"
for action in install configure; do
	expect_failure sh "$SCRIPT" "$action" --answers "$T/answers.env" </dev/null
	grep -qi 'recover' "$T/refusal.log"
done
volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$(compose ps -aq server)")
boot=$(docker create --network none --mount "type=volume,source=$volume,target=/data" -e ABYSS_SERVER_NAME=probe -e ABYSS_OPERATOR_NAME=CI -e ABYSS_OPERATOR_CONTACT=mailto:test@example.org -e ABYSS_PUBLIC_URL=https://kit.example.org "$IMAGE")
docker start "$boot" >/dev/null
tries=0
while [ "$(docker inspect -f '{{.State.Running}}' "$boot")" = true ] && [ "$tries" -lt 10 ]; do
	sleep 0.2
	tries=$((tries + 1))
done
docker logs "$boot" >"$T/refused-boot.log" 2>&1
boot_state=$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$boot")
docker rm -fv "$boot" >/dev/null
case "$boot_state" in 'exited '[1-9]*) ;; *) fail "normal boot did not refuse partial restore ($boot_state)" ;; esac
grep -Eiq 'pending|recover|interrupted' "$T/refused-boot.log"

interrupt phase/recovery-start KILL recover
expect_failure env TEST_FAIL_RECOVERY=1 sh "$SCRIPT" recover
[ -s "$DIR/.installer/pending" ]
interrupt phase/reverted KILL recover
sh "$SCRIPT" recover
check_data "$IMAGE" changed
# Once the successful status is committed, recovery must finish the operation.
interrupt phase/healthy KILL restore --yes "$T/before.tar"
sh "$SCRIPT" recover
check_data "$IMAGE" before

sh "$SCRIPT" upgrade "$good_tag"
admin import /import/sample-catalog >/dev/null
save newer
python3 - "$T/newer.tar" "$T/newer.db" <<'PY'
import sqlite3, sys, tarfile
from pathlib import Path
with tarfile.open(sys.argv[1]) as archive:
    Path(sys.argv[2]).write_bytes(archive.extractfile("abyssreader.db").read())
with sqlite3.connect(sys.argv[2]) as db:
    assert db.execute("SELECT value FROM successful_e2e_migration").fetchone() == ("new release",)
    assert db.execute("SELECT max(version_id) FROM goose_db_version WHERE is_applied").fetchone() == (99999,)
PY
# The script on disk is still stamped for the earlier release.
sh scripts/testdata/release-check.sh "$SCRIPT" "$T" "$good_tag"
expect_failure sh "$SCRIPT" upgrade 0.0.0
grep -qi 'downgrade' "$T/refusal.log"
expect_failure sh "$T/older-release.sh" upgrade
grep -qi 'downgrade' "$T/refusal.log"
printf 'no\n' | expect_failure sh "$T/current-release.sh" rollback

printf 'ROLLBACK\n' >"$T/rollback-answer"
(TEST_INPUT="$T/rollback-answer" interrupt candidate-probed KILL rollback)
inspect_orphan_cut rollback "$base_schema"
sh "$SCRIPT" recover
check_data "$good" newer

# Replace the older tag with an image that really fails after migration. A
# failed rollback must recover ALL newer rows, schema and images and keep its pin.
docker tag "$broken" "$IMAGE"
printf 'ROLLBACK\n' | expect_failure env TEST_FAST_RETRIES=1 sh "$T/current-release.sh" rollback
grep -qi 'status check' "$T/refusal.log"
check_data "$good" newer
docker image rm "$IMAGE" >/dev/null
if docker image inspect "$IMAGE" >/dev/null 2>&1; then fail "old tag was not evicted"; fi
printf 'ROLLBACK\n' | sh "$T/current-release.sh" rollback
check_data "$IMAGE" before
expect_failure sh "$SCRIPT" restore --yes "$T/newer.tar"
grep -Eiq 'schema|newer' "$T/refusal.log"
check_data "$IMAGE" before

admin import /import/sample-catalog >/dev/null
printf 'RESTORE\n' | sh "$SCRIPT" restore "$T/before.tar"
check_data "$IMAGE" before
expect_failure sh "$SCRIPT" uninstall </dev/null
printf 'y\n' | expect_failure sh "$SCRIPT" uninstall
check_data "$IMAGE" before
# Lose the entire host journal during a real partial rename. The project still
# has its labelled volume, and only a typed abandonment may clear its guard.
admin import /import/sample-catalog >/dev/null
save orphan-live
interrupt mid-swap KILL restore --yes "$T/before.tar"
cid=$(compose ps -aq server)
lost_project=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$cid")
[ -n "$lost_project" ] || fail "cannot identify test project before moving its installation"
damage_images
mv "$DIR" "$T/lost-installation"
expect_failure sh "$SCRIPT" recover </dev/null
cp "$T/refusal.log" "$T/orphan-info.log"
grep -Fq "$volume" "$T/orphan-info.log"
grep -Fq '.kit-operation' "$T/orphan-info.log"
grep -Fq '.kit-restore' "$T/orphan-info.log"
grep -Fqx 'Operation: restore' "$T/orphan-info.log"
grep -Fqx "Applied schema: $base_schema" "$T/orphan-info.log"
grep -Fqx "Pinned image supports schema: $base_schema" "$T/orphan-info.log"
grep -Fq 'Abandoning this restore' "$T/orphan-info.log"
grep -Eq 'Newest complete generation: /data/backups/kit-[^/]+/backup.json' "$T/orphan-info.log"
grep -Fq "$missing_image (missing)" "$T/orphan-info.log"
grep -Fq "$corrupt_image (checksum mismatch)" "$T/orphan-info.log"
grep -q -- '--abandon' "$T/orphan-info.log"
expect_failure sh "$SCRIPT" recover --abandon </dev/null
printf 'wrong\n' | expect_failure sh "$SCRIPT" recover --abandon
expect_failure sh "$SCRIPT" recover
grep -Fq '.kit-restore' "$T/refusal.log"
printf 'ABANDON\n' | sh "$SCRIPT" recover --abandon
[ "$(docker inspect -f '{{.State.Running}}' "$cid")" = false ] || fail "abandon unexpectedly started the server"
volume_fixture check-abandoned
volume_fixture abandon-note >"$T/abandon.json"
python3 - "$T/abandon.json" "$base_schema" <<'PYNOTE'
from datetime import datetime, timezone
import json, sys
note = json.load(open(sys.argv[1]))
assert set(note) == {"operation", "appliedSchema", "abandonedAt", "pinPath"}
schema = int(sys.argv[2])
assert note["operation"] == {"kind": "restore", "supportedSchema": schema}
assert note["appliedSchema"] == schema and note["pinPath"] == "none recorded"
age = datetime.now(timezone.utc) - datetime.fromisoformat(note["abandonedAt"].replace("Z", "+00:00"))
assert -60 < age.total_seconds() < 600
PYNOTE
sh "$SCRIPT" recover >"$T/idle-recover.log"
grep -Eqi "Last abandonment .*restore.*schema $base_schema.*rollback pin" "$T/idle-recover.log"
sh "$SCRIPT" --answers "$T/answers.env" >/dev/null
compose exec -T server abyssreader-server status | grep -Eqi "Last abandonment .*restore.*schema $base_schema.*rollback pin"
check_data "$IMAGE" orphan-live
sh "$SCRIPT" restore --yes "$T/before.tar" >/dev/null
check_data "$IMAGE" before

# Model the two retained volumes from a prior Caddy configuration.
project=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$(compose ps -q server)")
for label in caddy_config caddy_data; do
	name="$project-464-$$-$label"
	docker volume create --label "com.docker.compose.project=$project" --label "com.docker.compose.volume=$label" "$name" >/dev/null
done
printf 'n\nwrong address\n' | sh "$SCRIPT" uninstall >"$T/kept-volumes.log"
for label in caddy_config caddy_data; do
	name="$project-464-$$-$label"
	grep -Fx "$name" "$T/kept-volumes.log"
	docker volume inspect "$name" >/dev/null
done
[ ! -e "$DIR" ]
docker volume inspect "$volume" >/dev/null
docker tag "$TEST_BASE_DIGEST" "$IMAGE"
sh "$SCRIPT" --answers "$T/answers.env" >/dev/null
# The immediately preceding cover is damaged too; only an older retained
# copy can make this final archive complete enough for a normal restore.
save final-older
save final-newest
volume_fixture damage-newest "$missing_image"
damage_images
printf 'y\nhttps://kit.example.org\n' >"$T/uninstall-answers"
(TEST_INPUT="$T/uninstall-answers" interrupt phase/uninstalling KILL uninstall)
sh "$SCRIPT" recover >"$T/uninstall.log"

[ ! -e "$DIR" ]
if docker volume inspect "$volume" >/dev/null 2>&1; then fail "confirmed uninstall kept the volume"; fi
for label in caddy_config caddy_data; do
	if docker volume inspect "$project-464-$$-$label" >/dev/null 2>&1; then fail "confirmed uninstall kept $label"; fi
done
archive=$(sed -n 's/^Final backup: //p' "$T/KILL-phase-uninstalling.log" "$T/uninstall.log" | tail -1)
[ -s "$archive" ]
cp "$archive" "$T/repaired-final.tar"
compare before repaired-final
cleanup_final() {
	case "$1" in "$DIR"-final.????????/backup.tar) ;; *) fail "unexpected disposable archive path: $1" ;; esac
	rm -f "$1" "$(dirname "$1")/full-volume.tar"
	rmdir "$(dirname "$1")"
}
cleanup_final "$archive"

# Start from the repaired portable archive alone. Remove every local copy of
# the selected image so this independent installation truly cannot recover it.
reinstall_final() {
	docker tag "$TEST_BASE_DIGEST" "$IMAGE"
	sh "$SCRIPT" --answers "$T/answers.env" >/dev/null
	sh "$SCRIPT" restore --yes "$T/repaired-final.tar" >/dev/null
	check_data "$IMAGE" before
	volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$(compose ps -q server)")
}
reinstall_final
volume_fixture lose-every-copy "$missing_image"
printf 'y\nhttps://kit.example.org\n' | expect_failure sh "$SCRIPT" uninstall
grep -Fq 'DELETE INCOMPLETE' "$T/refusal.log"
grep -Fq "$missing_image" "$T/refusal.log"
docker volume inspect "$volume" >/dev/null
archive=$(sed -n 's/^Final backup: //p' "$T/refusal.log" | tail -1)
[ -s "$archive" ]
python3 scripts/testdata/check-preserved.py verify "$T/before.tar" "$archive" "$missing_image"
cleanup_final "$archive"
printf 'y\nhttps://kit.example.org\nDELETE INCOMPLETE\n' | sh "$SCRIPT" uninstall >"$T/partial-final.log" 2>&1
if docker volume inspect "$volume" >/dev/null 2>&1; then fail "typed partial deletion kept the volume"; fi
archive=$(sed -n 's/^Final backup: //p' "$T/partial-final.log" | tail -1)
python3 scripts/testdata/check-preserved.py verify "$T/before.tar" "$archive" "$missing_image"
docker run --rm -i --network none --tmpfs '/data:uid=65532,gid=65532,mode=0700' \
	"$base_cache" maintenance verify-preserved <"$archive"
cleanup_final "$archive"

# A corrupt database has a different deletion prompt and a full-volume salvage
# copy. Prove both archives survive deletion, including the exact torn sidecars.
reinstall_final
compose stop server
volume_fixture truncate-db >"$T/uninstall-torn.sha"
printf 'y\nhttps://kit.example.org\nDELETE INCOMPLETE\n' | expect_failure sh "$SCRIPT" uninstall
grep -Fq 'DELETE RAW' "$T/refusal.log"
docker volume inspect "$volume" >/dev/null
[ "$(docker inspect -f '{{.State.Running}}' "$(compose ps -aq server)")" = false ] || fail "raw deletion refusal started the damaged server"
archive=$(sed -n 's/^Final backup: //p' "$T/refusal.log" | tail -1)
python3 scripts/testdata/check-raw.py "$archive" "$T/uninstall-torn.sha" "$(dirname "$archive")/full-volume.tar"
cleanup_final "$archive"
printf 'y\nhttps://kit.example.org\nDELETE RAW\n' | sh "$SCRIPT" uninstall >"$T/raw-final.log" 2>&1
grep -Fq 'DELETE RAW' "$T/raw-final.log"
if docker volume inspect "$volume" >/dev/null 2>&1; then fail "typed raw deletion kept the volume"; fi
archive=$(sed -n 's/^Final backup: //p' "$T/raw-final.log" | tail -1)
python3 scripts/testdata/check-raw.py "$archive" "$T/uninstall-torn.sha" "$(dirname "$archive")/full-volume.tar"
cleanup_final "$archive"
