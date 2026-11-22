#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
set -eu
script=$1
work=$2/check-release
installed=$3
mkdir -p "$work"
# Only the public URL with no outgoing credentials, identifiers or query string is allowed.
cat >"$work/curl" <<'STUB'
#!/bin/sh
[ "$*" = "-q -fsS --proto =https --max-time 20 https://api.github.com/repos/IGlace/abyssreader-server-kit/releases/latest" ] || exit 99
printf '{"assets":[{"tag_name":"99.1.0"}],"tag_name":"%s"}\n' "$TEST_LATEST_RELEASE"
STUB
# Expose the system utilities and Docker, like a minimal host without Python.
for cmd in sh docker awk cat cut head id sed grep uname chmod chown mkdir flock rm ln; do
	path=$(command -v "$cmd") || continue
	ln -sf "$path" "$work/$cmd"
done
chmod +x "$work/curl"
export PATH="$work"
if command -v python3 >/dev/null 2>&1; then exit 1; fi
export TEST_LATEST_RELEASE=1.2.3
sh "$script" upgrade --check >"$work/online.log"
grep -F "$installed" "$work/online.log" | grep -qi 'installed'
grep -qx 'Latest kit release: 1.2.3' "$work/online.log"
grep -Eiq 'newer|update .*available' "$work/online.log"
TEST_LATEST_RELEASE=$installed
sh "$script" upgrade --check >"$work/current.log"
grep -Eiq 'up.to.date|already.*latest|is current' "$work/current.log"
TEST_LATEST_RELEASE=0.0.0
sh "$script" upgrade --check >"$work/older.log"
grep -qi 'installed kit is newer' "$work/older.log"
printf '#!/bin/sh\nexit 7\n' >"$work/curl"
if sh "$script" upgrade --check >"$work/offline.log" 2>&1; then exit 1; fi
grep -q 'release API is unavailable' "$work/offline.log"
