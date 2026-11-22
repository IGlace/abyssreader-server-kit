#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# A real installer upgrade on a private, named local-driver tmpfs volume.
set -eu
umask 077
work=$1
image=$2
candidate=$3
project="kit-budget-$$"
volume="$project-data"
dir="$work/budget-install"
mkdir -p "$dir/.installer"
export COMPOSE_PROJECT_NAME="$project" ABYSS_INSTALL_DIR="$dir" ABYSS_INSTALL_TEST=1 ABYSS_INSTALL_WAIT=15
compose() { (cd "$dir" && docker compose "$@"); }
if docker volume inspect "$volume" >/dev/null 2>&1; then exit 1; fi
docker volume create --driver local --opt type=tmpfs --opt device=tmpfs \
	--opt o=size=128m,uid=65532,gid=65532,mode=0700 "$volume" >/dev/null
holder=""
cleanup() {
	compose down >/dev/null 2>&1 || true
	if [ -n "$holder" ]; then docker rm -f "$holder" >/dev/null 2>&1 || true; fi
	docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
# Keep the tmpfs mounted across service replacement and maintenance containers.
holder=$(docker run -d --network none --mount "type=volume,source=$volume,target=/data" \
	--mount "type=bind,source=$work/fill-test,target=/fixture,readonly" --entrypoint /fixture "$image" hold)
cat >"$dir/.env" <<'ENV'
ABYSS_PUBLIC_URL=https://budget.example.org
ABYSS_OPERATOR_NAME=CI
ABYSS_OPERATOR_CONTACT=mailto:test@example.org
ABYSS_SERVER_NAME=budget
ABYSS_BACKUP_KEEP=2
ABYSS_BACKUP_SCHEDULE=off
ENV
cat >"$dir/compose.yaml" <<YAML
services:
  server:
    image: $image
    env_file: .env
    volumes:
      - data:/data
volumes:
  data:
    external: true
    name: $volume
YAML
echo 'HTTPS=own-proxy' >"$dir/.installer/install.info"
sh scripts/stamp-install.sh --keep-test-hooks "${image##*:}" "$work/budget-install.sh" >/dev/null
compose up -d >/dev/null
tries=0
until compose exec -T server abyssreader-server status >/dev/null 2>&1; do
	[ "$tries" -lt 30 ] || exit 1
	tries=$((tries + 1))
	sleep 0.2
done
docker exec "$holder" /fixture seed-budget
save() {
	gen=$(compose exec -T server abyssreader-server admin backup now)
	compose exec -T server abyssreader-server admin backup export "$gen" >"$work/budget-$1.tar"
}
save before
docker exec "$holder" /fixture accepted-headroom
if TEST_FAST_RETRIES=1 sh "$work/budget-install.sh" upgrade "$candidate" >"$work/budget-upgrade.log" 2>&1; then exit 1; fi
grep -q 'status check' "$work/budget-upgrade.log"
compose exec -T server abyssreader-server status
[ "$(docker inspect -f '{{.Config.Image}}' "$(compose ps -q server)")" = "$image" ]
docker cp "$holder:/data/e2e-migration-copied" "$work/copied-ballast"
[ "$(cat "$work/copied-ballast")" = 4194304 ]
save after
python3 - "$work" <<'PY'
from pathlib import Path
import sqlite3, sys, tarfile
folder = Path(sys.argv[1])
def snapshot(label):
    with tarfile.open(folder / f"budget-{label}.tar") as archive:
        path = folder / f"budget-{label}.db"
        path.write_bytes(archive.extractfile("abyssreader.db").read())
    with sqlite3.connect(path) as db:
        assert db.execute("SELECT length(payload) FROM e2e_ballast").fetchone() == (4194304,)
        return list(db.iterdump())
assert snapshot("before") == snapshot("after"), "budgeted upgrade lost rows or kept failed migration"
PY
