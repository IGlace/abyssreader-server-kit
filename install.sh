#!/bin/sh
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
#
# install.sh: install and configure an AbyssReader server (the server kit) on a Linux machine
# with Docker. Download it from the kit's release page, read it, then run it as root:
#
#   sudo sh install.sh                        install (asks the setup questions)
#   sudo sh install.sh configure              change the settings (the questions again, with the
#                                             current answers filled in), then restart
#   sudo sh install.sh --answers FILE         install without questions (an answers file)
#   sudo sh install.sh configure --answers FILE
#
#   sudo sh install.sh upgrade [VERSION]       snapshot, upgrade and verify
#   sudo sh install.sh upgrade --check         compare the installed and latest kit
#   sudo sh install.sh rollback                restore the pre-upgrade image and data
#   sudo sh install.sh restore [--yes] [--allow-definition-rollback] FILE
#                                          restore a verified server archive
#   sudo sh install.sh recover [--abandon]     recover or inspect an orphaned data volume
#   sudo sh install.sh uninstall               remove the installation
#
# Everything is described in docs/install.md of the kit. In short:
# - The server goes into /opt/abyssreader-server/ (compose.yaml, .env, Caddyfile, import/) and
#   its data into the Docker volume abyssreader-server_data. Both stay when you run this again.
# - The questions are asked by the server image itself (abyssreader-server setup), so they and
#   their checks are the same everywhere; this script only moves files and starts containers.
# - This script contacts: ghcr.io (pinned release images), GitHub's HTTPS release API and the app
#   support schedule on upgrade --check and, in the Caddy mode, Docker Hub
#   (the Caddy image) and Let's Encrypt (Caddy's certificate, by Caddy). It downloads and runs
#   Docker's own install script (https://get.docker.com) only when Docker is missing and you
#   say yes. The server itself contacts no one, except the error-reporting DSN if you set one.
# - Nothing secret is printed, put in a command line or left in your shell history.
set -eu
umask 077

# scripts/stamp-install.sh writes the version and the published image digest.
KIT_VERSION=unreleased
KIT_IMAGE_DIGEST=unreleased
KIT_IMAGE_REPO=ghcr.io/iglace/abyssreader-server-kit
IMAGE="$KIT_IMAGE_REPO:$KIT_VERSION"
case "$KIT_IMAGE_DIGEST" in sha256:*) IMAGE="$IMAGE@$KIT_IMAGE_DIGEST" ;; esac

# The oldest Docker Compose this kit is checked against (docs/install.md).
MIN_COMPOSE=2.24.0

INSTALL_DIR=/opt/abyssreader-server
WAIT_SECONDS=120
CUSTOM_IMAGE=""
TEST_MODE=""
# BEGIN TEST HOOKS. For the kit's own tests only (scripts/test-install.sh): another folder, a
# shorter wait, and running without root on a machine that is not Linux. A release copy has
# none of this: scripts/stamp-install.sh removes these lines.
INSTALL_DIR=${ABYSS_INSTALL_DIR:-$INSTALL_DIR}
WAIT_SECONDS=${ABYSS_INSTALL_WAIT:-$WAIT_SECONDS}
TEST_MODE=${ABYSS_INSTALL_TEST:-}
# END TEST HOOKS

STATE_DIR="$INSTALL_DIR/.installer"
PREVIOUS_DIR="$STATE_DIR/previous"
LOCK_DIR="$STATE_DIR/lock"
WORK_DIR=""
PENDING="$STATE_DIR/pending"
ARMING="$STATE_DIR/arming"
JOURNALED=""
RECOVERING=""
PREVIEW=""
ABANDON=""
HAVE_LOCK=""
# The setup's container, named so that it can be removed when this script is stopped.
SETUP_CONTAINER="abyssreader-setup-$$"
# What a signal interrupts: "" (nothing to undo), "configure-start" (the new settings are
# starting: go back to the previous ones) or "install-start" (a fresh install is starting).
PHASE=""

say() { printf '%s\n' "$*"; }
warn() { printf 'Warning: %s\n' "$*" >&2; }
die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage:
  sudo sh install.sh [install] [--answers FILE]   install the AbyssReader server
  sudo sh install.sh configure [--answers FILE]   change its settings, then restart it
  sudo sh install.sh upgrade [X.Y.Z | --check]
  sudo sh install.sh rollback
  sudo sh install.sh restore [--yes] [--allow-definition-rollback] BACKUP.tar
  sudo sh install.sh recover [--abandon]
  sudo sh install.sh uninstall
  sh install.sh help

Without --answers the setup asks its questions in this terminal. An answers file has the
.env format (KEY=value lines); docs/install.md lists its settings.
EOF
}

cleanup() {
	exit_status=$?
	trap - EXIT
	trap '' INT TERM HUP
	if [ -n "$JOURNALED" ] && { [ -f "$PENDING" ] || [ -f "$ARMING" ]; }; then
		if [ -z "$RECOVERING" ]; then
			trap '' PIPE
			# A disconnected controlling terminal must not control recovery or its children.
			if (recover_operation) </dev/null >>"$STATE_DIR/recovery.log" 2>&1; then
				(say "Recovery completed. See $STATE_DIR/recovery.log.") 2>/dev/null || true
			else
				(warn "Recovery pending. Run install.sh recover. See $STATE_DIR/recovery.log.") 2>/dev/null || true
			fi
		fi
		WORK_DIR=""
		[ "$exit_status" != 0 ] || exit_status=1
	fi
	if [ -n "$PREVIEW" ]; then
		helper discard-preview "$PREVIEW" </dev/null >/dev/null 2>&1 || true
	fi
	if [ -n "$HAVE_LOCK" ]; then
		docker rm -f "$SETUP_CONTAINER" >/dev/null 2>&1 || true
	fi
	if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
		rm -rf "$WORK_DIR"
	fi
	if [ -n "$HAVE_LOCK" ]; then
		rm -rf "$LOCK_DIR"
		HAVE_LOCK=""
	fi
	exit "$exit_status"
}

# on_signal CODE: Ctrl-C (130), SIGTERM (143) or a closed SSH session (SIGHUP, 129). The setup
# container and the work folder go; new settings that were starting are rolled back.
on_signal() {
	trap '' INT TERM HUP
	set +e
	if [ -n "$JOURNALED" ]; then exit "$1"; fi
	case "$PHASE" in
	configure-start)
		say ""
		say "Interrupted while the new settings were starting: going back to the previous ones."
		restore_previous
		if start; then
			say "The server runs with the previous settings again; the new ones were not kept."
		else
			say "The server does not start with the previous settings either."
			logs_hint
		fi
		;;
	install-start)
		say ""
		say "Interrupted while the server was starting: it is stopped. The settings are in $INSTALL_DIR/.env;"
		say "run sudo sh install.sh again to start it."
		compose down >/dev/null 2>&1
		;;
	*)
		say ""
		say "Interrupted: nothing was changed."
		;;
	esac
	exit "$1"
}
trap cleanup EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM
trap 'on_signal 129' HUP

# ask_yes_no QUESTION: 0 for yes. The answer is no without a terminal, and by default.
ask_yes_no() {
	if [ ! -t 0 ] || [ -n "${ANSWERS:-}" ]; then
		return 1
	fi
	prompt_yes_no "$1"
}

prompt_yes_no() {
	printf '%s [y/N]: ' "$1"
	read -r ask_reply || return 1
	case "$ask_reply" in
	y | Y | yes | YES | Yes) return 0 ;;
	esac
	return 1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# info_get KEY: a value of the setup's install.info (never secret).
info_get() {
	sed -n "s/^$1=//p" "$STATE_DIR/install.info" 2>/dev/null | head -n 1
}

compose() {
	(cd "$INSTALL_DIR" && docker compose "$@")
}

# own PATH MODE: root-owned with this mode.
own() {
	if [ -z "$TEST_MODE" ]; then
		chown root:root "$1"
	fi
	chmod "$2" "$1"
}

check_root() {
	if [ -z "$TEST_MODE" ]; then
		[ "$(id -u)" = 0 ] || die "run it as root: sudo sh install.sh"
		[ "$(uname -s)" = Linux ] || die "this installer is for Linux machines (a VPS with Docker)."
	fi
}

check_release_image() {
	if [ -z "$CUSTOM_IMAGE" ] && { [ "$KIT_VERSION" = unreleased ] || { [ "$KIT_IMAGE_DIGEST" != local-test ] && ! valid_digest "$KIT_IMAGE_DIGEST"; }; }; then
		die "this copy of install.sh is not from a kit release (version or image digest is missing).
Download install.sh from the kit's release page: it installs exactly that release."
	fi
}

offer_docker() {
	say ""
	say "Docker is not installed. The server runs in Docker containers, so it needs Docker Engine"
	say "with the Compose plugin. Two ways to get it:"
	say "  1. Choose your host's Docker one-click image (Hetzner, DigitalOcean, Contabo, OVH and others"
	say "     offer one) for a new machine, then run this script again there."
	say "  2. Let this script download Docker's own install script from https://get.docker.com and run"
	say "     it. It adds Docker's package repository (download.docker.com) and installs Docker Engine"
	say "     and Compose with your system's package manager."
	if ! ask_yes_no "Download and run Docker's install script now"; then
		die "Docker is missing: install it (see above), then run this script again."
	fi
	WORK_DIR=$(mktemp -d)
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL https://get.docker.com -o "$WORK_DIR/get-docker.sh"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$WORK_DIR/get-docker.sh" https://get.docker.com
	else
		die "neither curl nor wget is installed to download Docker's script: install one (apt-get install curl)."
	fi
	say "Running Docker's install script (a copy is in $WORK_DIR/get-docker.sh while it runs)."
	sh "$WORK_DIR/get-docker.sh"
	rm -rf "$WORK_DIR"
	WORK_DIR=""
}

check_docker() {
	if ! command -v docker >/dev/null 2>&1; then
		offer_docker
		command -v docker >/dev/null 2>&1 || die "Docker is still missing after its install script."
	fi
	docker info >/dev/null 2>&1 || die "Docker is installed but does not answer. Start it (systemctl start docker) and run this again."
	compose_fix="Install Docker's own packages, which include Compose: on Debian and Ubuntu that replaces the
distribution's docker.io (Debian 12 has no Compose v2 package of its own). Docker's script does it:
  curl -fsSL https://get.docker.com -o get-docker.sh && sudo sh get-docker.sh
or, with Docker's packages already installed: apt-get install docker-compose-plugin
(on Ubuntu 24.04 with the distribution's docker.io: apt-get install docker-compose-v2)."
	compose_version=$(docker compose version --short 2>/dev/null) || die "Docker Compose v2 (docker compose) is missing.
$compose_fix"
	compose_version=${compose_version#v}
	if ! version_at_least "$compose_version" "$MIN_COMPOSE"; then
		die "Docker Compose $compose_version is too old: this kit needs $MIN_COMPOSE or later. Nothing was changed.
$compose_fix"
	fi
}

# version_at_least A B: 0 when version A (x.y.z, anything after a "-" or "+" ignored) >= B.
version_at_least() {
	printf '%s\n%s\n' "$1" "$2" | awk -F. '
		{ sub(/[-+].*/, ""); for (i = 1; i <= 3; i++) v[NR, i] = ($i == "" ? 0 : $i) + 0 }
		END {
			for (i = 1; i <= 3; i++) {
				if (v[1, i] > v[2, i]) exit 0
				if (v[1, i] < v[2, i]) exit 1
			}
			exit 0
		}'
}

lock() {
	mkdir -p "$INSTALL_DIR" "$STATE_DIR"
	own "$INSTALL_DIR" 755
	own "$STATE_DIR" 700
	# Serialize stale-lock takeover too: two contenders can both observe a dead PID.
	# Linux flock on this directory descriptor is released by the kernel on exit.
	if [ "$(uname -s)" = Linux ]; then
		command -v flock >/dev/null || die "the installer lock requires flock (util-linux)."
		exec 9<"$STATE_DIR"
		flock -n 9 || die "another install.sh holds the installer lock."
	fi
	if ! mkdir "$LOCK_DIR" 2>/dev/null; then
		holder=$(cat "$LOCK_DIR/pid" 2>/dev/null || true)
		case "$holder" in *[!0-9]* | "") holder="" ;; esac
		if [ -n "$holder" ] && kill -0 "$holder" 2>/dev/null; then
			die "another install.sh (process $holder) is running on this server. If none is, remove $LOCK_DIR."
		fi
		say "Taking over the lock of an install.sh that stopped (process ${holder:-unknown} is gone), and removing what it left."
		if [ -n "$holder" ]; then
			rm -rf "$STATE_DIR/setup.$holder"
			docker rm -f "abyssreader-setup-$holder" >/dev/null 2>&1 || true
		elif [ "$(uname -s)" != Linux ]; then
			die "installer lock has no pid; this test host cannot prove the lock is stale."
		fi
		rm -rf "$LOCK_DIR"
		mkdir "$LOCK_DIR" 2>/dev/null || die "another install.sh took the lock just now."
	fi
	HAVE_LOCK=1
	printf '%s\n' "$$" >"$LOCK_DIR/pid"
}

# Keep tags that this installation actually used, including a former proxy or helper.
remember_image() {
	if [ -f "$STATE_DIR/used-images" ] && grep -Fqx -- "$1" "$STATE_DIR/used-images"; then return 0; fi
	printf '%s\n' "$1" >>"$STATE_DIR/used-images"
}

remember_proxy() {
	proxy_id=$(compose ps -aq caddy 2>/dev/null) || return 0
	if [ -n "$proxy_id" ]; then
		proxy_tag=$(docker inspect -f '{{.Config.Image}}' "$proxy_id") || return 1
		remember_image "$proxy_tag"
	fi
}

valid_digest() {
	case "$1" in sha256:*) hex=${1#sha256:} ;; *) return 1 ;; esac
	case "$hex" in *[!0-9a-f]*) return 1 ;; esac
	[ "${#hex}" -eq 64 ]
}

image_cached() {
	case "$1" in
	*'@'*)
		cached_name=${1%@*}
		cached_digest=${1##*@}
		valid_digest "$cached_digest" || return 1
		case "${cached_name##*/}" in *:*) cached_name=${cached_name%:*} ;; esac
		docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$1" 2>/dev/null |
			grep -Fxq "$cached_name@$cached_digest"
		;;
	*) docker image inspect "$1" >/dev/null 2>&1 ;;
	esac
}

pull_image() {
	if image_cached "$1"; then return; fi
	say "Downloading $1"
	docker pull "$1" >/dev/null || die "could not download $1. Is the machine online, and is this a published kit version?"
	image_cached "$1" || die "Docker does not have the requested image reference $1 after pulling it."
}

host_addresses() {
	if hostname -I >/dev/null 2>&1; then
		hostname -I
	elif command -v ip >/dev/null 2>&1; then
		ip -o addr show | awk '{print $4}'
	fi
}

# Read the dedicated image setting without evaluating the answers file as shell code.
# This setting accepts a literal pinned ref, optionally surrounded by quotes.
read_custom_image() {
	[ -f "$1" ] || return 0
	LC_ALL=C awk '
		/^[[:space:]]*KIT_IMAGE[[:space:]]*=/ {
			if (seen++) exit 1
			sub(/^[^=]*=[[:space:]]*/, "")
			sub(/[[:space:]]*$/, "")
			if (substr($0,1,1) == "\"" && substr($0,length,1) == "\"") $0 = substr($0,2,length-2)
			if (substr($0,1,1) == "\047" && substr($0,length,1) == "\047") $0 = substr($0,2,length-2)
			if ($0 !~ /^[a-z0-9][a-zA-Z0-9._:\/-]*(:[a-zA-Z0-9_][a-zA-Z0-9_.-]*|@sha256:[a-f0-9]+)$/ ||
			    length($0) > 326 || $0 ~ /:latest$/ || ($0 ~ /@/ && length($0)-index($0,"@") != 71)) exit 1
			print
		}
	' "$1"
}

select_custom_image() {
	CUSTOM_IMAGE=$(read_custom_image "$INSTALL_DIR/.env") || die "invalid KIT_IMAGE in the installed .env."
	requested=$(read_custom_image "$ANSWERS") || die "KIT_IMAGE needs one literal image reference pinned by tag or SHA-256 digest."
	if [ -f "$INSTALL_DIR/.env" ] && [ -n "$requested" ] && [ "$requested" != "$CUSTOM_IMAGE" ]; then
		die "KIT_IMAGE selects the image on a fresh install. See the fork-and-own-image guide to change an installed image."
	fi
	CUSTOM_IMAGE=${CUSTOM_IMAGE:-$requested}
	if [ -n "$CUSTOM_IMAGE" ] && [ "$CUSTOM_IMAGE" != "$IMAGE" ]; then
		warn "Using custom kit image $CUSTOM_IMAGE. Support covers official images only."
		if [ -t 0 ] && ! prompt_yes_no "Continue with this custom image"; then
			die "Custom image declined. Nothing was changed."
		fi
		IMAGE=$CUSTOM_IMAGE
	fi
}

# run_setup: the questions (or the answers file), inside the server image. The work folder is
# root-only; the container runs as root with no capabilities and a read-only file system.
run_setup() {
	WORK_DIR="$STATE_DIR/setup.$$"
	rm -rf "$WORK_DIR"
	mkdir -m 700 "$WORK_DIR"
	if [ -f "$INSTALL_DIR/.env" ]; then
		cp "$INSTALL_DIR/.env" "$WORK_DIR/current.env"
	fi
	if [ -n "${ANSWERS:-}" ]; then
		cp "$ANSWERS" "$WORK_DIR/answers.env"
	fi
	host_addresses >"$WORK_DIR/host-addresses" 2>/dev/null || true
	pull_image "$IMAGE"
	setup_tty=-i
	if [ -t 0 ] && [ -t 1 ] && [ -z "${ANSWERS:-}" ]; then
		setup_tty=-it
	fi
	setup_status=0
	docker run --rm --name "$SETUP_CONTAINER" "$setup_tty" --user 0:0 --cap-drop ALL --security-opt no-new-privileges:true \
		--read-only --tmpfs /tmp -v "$WORK_DIR:/work" "$IMAGE" setup --work /work --image "$IMAGE" || setup_status=$?
	case "$setup_status" in
	0) ;;
	3) die "nothing was changed." ;;
	4) die "the setup was interrupted; nothing was changed." ;;
	*) die "the setup did not finish (see above); nothing was changed." ;;
	esac
	for f in .env compose.yaml install.info; do
		[ -s "$WORK_DIR/out/$f" ] || die "the setup wrote no $f; nothing was changed."
	done
}

# was_edited NAME: 0 when the installed NAME (compose.yaml, Caddyfile) is not the one this
# script last wrote: the operator changed it.
was_edited() {
	[ -f "$INSTALL_DIR/$1" ] || return 1
	[ -f "$STATE_DIR/$1.sha256" ] || return 0
	[ "$(sha256_of "$INSTALL_DIR/$1")" != "$(cat "$STATE_DIR/$1.sha256")" ]
}

# decide NAME: sets CHOICE to replace or keep. An edited file the new settings would change
# (or remove) is replaced only on the operator's yes; it is then kept as NAME.edited-<time>.
decide() {
	CHOICE=replace
	was_edited "$1" || return 0
	if [ -f "$WORK_DIR/out/$1" ] && cmp -s "$WORK_DIR/out/$1" "$INSTALL_DIR/$1"; then
		return 0
	fi
	say ""
	say "$INSTALL_DIR/$1 was changed since install.sh wrote it."
	if ask_yes_no "Replace it with the one for these settings (yours is kept as $1.edited-<date and time>)"; then
		backup_edited "$1"
	else
		CHOICE=keep
		warn "your $1 is kept: changes of the HTTPS mode, the address or the port may not apply until you make them there."
	fi
}

backup_edited() {
	edited_copy="$INSTALL_DIR/$1.edited-$(date +%Y%m%d-%H%M%S)"
	cp "$INSTALL_DIR/$1" "$edited_copy"
	own "$edited_copy" 600
	say "Your $1 is kept as $edited_copy."
}

# record NAME: remember the checksum of the NAME this script wrote.
record() {
	sha256_of "$INSTALL_DIR/$1" >"$STATE_DIR/$1.sha256"
	own "$STATE_DIR/$1.sha256" 600
}

# keep_previous: a copy of the installed files, to go back to when the new ones do not start.
keep_previous() {
	rm -rf "$PREVIOUS_DIR"
	mkdir -m 700 "$PREVIOUS_DIR"
	for f in .env compose.yaml Caddyfile; do
		if [ -f "$INSTALL_DIR/$f" ]; then
			cp "$INSTALL_DIR/$f" "$PREVIOUS_DIR/$f"
		fi
	done
	for f in compose.yaml.sha256 Caddyfile.sha256 install.info; do
		rm -f "$PREVIOUS_DIR/$f"
		if [ -f "$STATE_DIR/$f" ]; then
			cp "$STATE_DIR/$f" "$PREVIOUS_DIR/$f"
		fi
	done
}

# put SRC NAME MODE: move a new file into the install folder in one step.
put() {
	own "$1" "$3"
	mv -f "$1" "$INSTALL_DIR/$2"
}

# install_files COMPOSE_CHOICE CADDY_CHOICE: the setup's files into /opt/abyssreader-server
# (replace or keep each of compose.yaml and the Caddyfile). .env goes last: its presence means
# "installed".
install_files() {
	if [ "$1" = replace ]; then
		put "$WORK_DIR/out/compose.yaml" compose.yaml 644
		record compose.yaml
	fi
	if [ "$2" = replace ]; then
		if [ -f "$WORK_DIR/out/Caddyfile" ]; then
			put "$WORK_DIR/out/Caddyfile" Caddyfile 644
			record Caddyfile
		else
			rm -f "$INSTALL_DIR/Caddyfile" "$STATE_DIR/Caddyfile.sha256"
		fi
	fi
	mv -f "$WORK_DIR/out/install.info" "$STATE_DIR/install.info"
	own "$STATE_DIR/install.info" 600
	put "$WORK_DIR/out/.env" .env 600
	mkdir -p "$INSTALL_DIR/import"
	own "$INSTALL_DIR/import" 755
}

restore_previous() {
	for f in .env compose.yaml Caddyfile; do
		if [ -f "$PREVIOUS_DIR/$f" ]; then
			cp "$PREVIOUS_DIR/$f" "$INSTALL_DIR/$f.restore"
			case "$f" in .env) mode=600 ;; *) mode=644 ;; esac
			put "$INSTALL_DIR/$f.restore" "$f" "$mode"
		else
			rm -f "$INSTALL_DIR/$f"
		fi
	done
	for f in compose.yaml.sha256 Caddyfile.sha256 install.info; do
		if [ -f "$PREVIOUS_DIR/$f" ]; then
			cp "$PREVIOUS_DIR/$f" "$STATE_DIR/$f"
			own "$STATE_DIR/$f" 600
		else
			rm -f "$STATE_DIR/$f"
		fi
	done
}

# start: (re)create the containers with the installed files, then wait for /api/status.
start() {
	if [ "$(info_get HTTPS)" = caddy ]; then
		say "Starting the server and Caddy."
	else
		say "Starting the server."
	fi
	compose up -d --force-recreate --remove-orphans || return 1
	wait_status
}

wait_status() {
	say "Waiting for the server to answer /api/status (up to $WAIT_SECONDS seconds)."
	waited=0
	while [ "$waited" -lt "$WAIT_SECONDS" ]; do
		if STATUS_LINE=$(compose exec -T server abyssreader-server status 2>/dev/null); then
			say "The server answers: $STATUS_LINE"
			return 0
		fi
		sleep 2
		waited=$((waited + 2))
	done
	return 1
}

logs_hint() {
	say "Read what the server says with:"
	say "  cd $INSTALL_DIR && sudo docker compose logs --tail 100 server"
	if [ "$(info_get HTTPS)" = caddy ]; then
		say "  cd $INSTALL_DIR && sudo docker compose logs --tail 100 caddy"
	fi
}

# check_https: in the Caddy mode, whether the public address answers yet (the certificate
# can take a minute). Only a warning: the server runs either way.
check_https() {
	[ "$(info_get HTTPS)" = caddy ] || return 0
	command -v curl >/dev/null 2>&1 || return 0
	url="$(info_get PUBLIC_URL)/api/status"
	say "Checking $url (Caddy is getting its certificate)."
	tries=0
	while [ "$tries" -lt 12 ]; do
		if curl -fsS --max-time 10 -o /dev/null "$url" 2>/dev/null; then
			say "$url answers over HTTPS."
			return 0
		fi
		sleep 5
		tries=$((tries + 1))
	done
	warn "$url does not answer over HTTPS yet. Check that $(info_get HOST) points at this machine and that
ports 80 and 443 are open (firewall, host's network rules); Caddy keeps trying by itself."
	logs_hint
}

next_steps() {
	remember_proxy
	url=$(info_get PUBLIC_URL)
	say ""
	say "Your AbyssReader server is running: $url"
	if [ "$(info_get HTTPS)" = own-proxy ]; then
		say "  - Point your proxy (nginx, Cloudflare Tunnel...) at http://127.0.0.1:$(info_get PORT) and have it"
		say "    pass X-Forwarded-For. Users add $url in the app."
	else
		say "  - Users add $url in the app."
	fi
	if [ "$(info_get ACCESS_KEY)" = yes ]; then
		if [ "$(info_get ACCESS_KEY_CHANGED)" = yes ]; then
			say "  - The server has a NEW access key: users type it when they add the server. Read it with"
		else
			say "  - Users need the access key when they add the server. Read it with"
		fi
		say "      sudo grep '^ABYSS_ACCESS_KEY=' $INSTALL_DIR/.env"
	fi
	say "  - Fill the catalog: put an import folder in $INSTALL_DIR/import/, then"
	say "      cd $INSTALL_DIR && sudo docker compose exec server abyssreader-server admin import /import/<folder>"
	say "  - Change the settings: sudo sh install.sh configure"
	say "  - Logs: cd $INSTALL_DIR && sudo docker compose logs -f server"
}

check_answers_file() {
	[ -f "$ANSWERS" ] || die "no answers file at $ANSWERS"
	if [ -n "$(find "$ANSWERS" -prune \( -perm -004 -o -perm -040 \) 2>/dev/null)" ]; then
		# A DSN, or an access key other than generate/new/empty, is a secret others could read.
		if grep -Eq '^[[:space:]]*(export[[:space:]]+)?ABYSS_ERROR_REPORTING_DSN[[:space:]]*=[[:space:]]*[^[:space:]#"'"'"']' "$ANSWERS" ||
			grep -Eq '^[[:space:]]*(export[[:space:]]+)?ABYSS_ERROR_REPORTING_DSN[[:space:]]*=[[:space:]]*["'"'"'][^"'"'"']' "$ANSWERS" ||
			grep -E '^[[:space:]]*(export[[:space:]]+)?ABYSS_ACCESS_KEY[[:space:]]*=' "$ANSWERS" |
			sed -e 's/^[^=]*=[[:space:]]*//' -e 's/[[:space:]]*\(#.*\)\{0,1\}$//' -e "s/^[\"']//" -e "s/[\"']\$//" |
				grep -Evq '^(generate|new)?$'; then
			die "$ANSWERS holds a secret (the DSN or an access key) and other users can read it: chmod 600 it, then run this again."
		fi
		warn "$ANSWERS can be read by other users (chmod 600 it)."
	fi
}

do_install() {
	if [ -f "$INSTALL_DIR/.env" ]; then
		say "An AbyssReader server is already installed in $INSTALL_DIR; its settings and data stay as they are."
		if [ -n "${ANSWERS:-}" ]; then
			warn "the answers file was not used: an installed server takes new answers with
  sudo sh install.sh configure --answers $ANSWERS"
		else
			say "To change its settings: sudo sh install.sh configure"
		fi
		installed=$(sed -n 's/^    image: //p' "$INSTALL_DIR/compose.yaml" 2>/dev/null | head -n 1)
		if [ -n "$installed" ] && [ "$installed" != "$IMAGE" ]; then
			say "It runs $installed; this script is for $IMAGE. Upgrading is a separate command (install.sh upgrade)."
		fi
		if [ -n "$installed" ]; then
			pull_image "$installed"
		fi
		if ! compose up -d || ! wait_status; then
			logs_hint
			die "the server does not answer."
		fi
		next_steps
		return 0
	fi

	if docker volume inspect abyssreader-server_data >/dev/null 2>&1; then
		say "Note: the data volume abyssreader-server_data already exists (an earlier install): the server will use its data."
	fi
	if [ -f "$INSTALL_DIR/compose.yaml" ] && ! ask_yes_no "$INSTALL_DIR/compose.yaml exists without a .env. Replace it (a copy is kept in $PREVIOUS_DIR)"; then
		die "$INSTALL_DIR/compose.yaml was left as it is; nothing was changed."
	fi
	run_setup
	keep_previous
	PHASE=install-start
	install_files replace replace
	if start; then
		PHASE=""
		check_https
		next_steps
		return 0
	fi
	say ""
	PHASE=""
	say "The server did not start with these settings; it is stopped. The settings are in $INSTALL_DIR/.env."
	logs_hint
	compose down >/dev/null 2>&1 || true
	die "fix what the log names (sudo sh install.sh configure asks the questions again), then run install.sh again."
}

do_configure() {
	[ -f "$INSTALL_DIR/.env" ] || die "no server is installed in $INSTALL_DIR: run sudo sh install.sh first."
	IMAGE=$(installed_image)
	remember_proxy
	run_setup
	decide compose.yaml
	compose_choice=$CHOICE
	decide Caddyfile
	keep_previous
	PHASE=configure-start
	install_files "$compose_choice" "$CHOICE"
	if start; then
		PHASE=""
		rm -rf "$PREVIOUS_DIR"
		check_https
		next_steps
		return 0
	fi
	say ""
	say "The server did not start with the new settings. Going back to the previous ones."
	logs_hint
	restore_previous
	PHASE=""
	if start; then
		die "the new settings were not kept; the server runs with the previous ones again."
	fi
	die "the server does not start with the previous settings either."
}

# The helper comes from this script's exact release. It needs only the named volume,
# including while the service is stopped or its replacement image cannot run.
helper() {
	maintenance_name=$SETUP_CONTAINER-maintenance
	if [ -n "$JOURNALED" ]; then maintenance_name=abyssreader-${op:?}; fi
	docker run --rm -i --name "$maintenance_name" --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true \
		--mount "type=volume,source=$DATA_VOLUME,target=/data" "$IMAGE" maintenance "$@"
}

valid_tag() {
	case "$1" in '' | *[!0-9.rc-]*) return 1 ;; esac
	printf '%s\n' "$1" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$'
}

# Compare two validated kit tags. Compare decimal strings by length then lexically
# so an arbitrarily long valid version component cannot overflow shell arithmetic.
release_order() {
	awk -v a="$1" -v b="$2" 'function num(x,y) {
  if(length(x)!=length(y))return length(x)>length(y)?1:-1
  return ("x"x)>("x"y)?1:(("x"x)<("x"y)?-1:0)
 } BEGIN {
  split(a,A,/[-.]/);split(b,B,/[-.]/)
  for(i=1;i<=3;i++){c=num(A[i],B[i]);if(c){print c;exit}}
  if(A[4]!=B[4]){print (A[4]==""?1:(B[4]==""?-1:(A[4]=="rc"?1:-1)));exit}
  print num(A[5],B[5])
 }'
}

# Read GitHub's top-level tag_name. A release body or a nested asset can contain JSON
# text too. Tokenize strings with escapes and keep container depth while scanning.
release_check() {
	current=$(installed_image) || die "cannot read the installed image."
	case "$current" in
	'' | -* | *[!a-zA-Z0-9._/:@-]*) die "cannot identify the installed image." ;;
	"$KIT_IMAGE_REPO":*) installed_tag "$current" ;;
	*)
		say "Installed kit uses a custom image. Check your build for updates."
		support_check "$current"
		return ;;
	esac
	support_check "$current"
	release_json=$(curl -q -fsS --proto '=https' --max-time 20 https://api.github.com/repos/IGlace/abyssreader-server-kit/releases/latest) ||
		die "GitHub's release API is unavailable. The kit repository must be public and have a published release. Check that this machine can reach GitHub."
	latest=$(printf '%s\n' "$release_json" | awk '
 {line=$0
  while(length(line)){
   ch=substr(line,1,1)
   if(ch=="\""){
    if(!match(line,/^"([^"\\]|\\.)*"/)) exit 1
    word=substr(line,2,RLENGTH-2);line=substr(line,RLENGTH+1)
    if(depth==1 && state==2){print word;exit}
    state=(depth==1 && word=="tag_name")?1:0
   }else{
    line=substr(line,2)
    if(ch~/[[:space:]]/)continue
    if(ch==":" && state==1){state=2;continue}
    state=0
    if(ch=="{"||ch=="[")depth++
    if(ch=="}"||ch=="]")depth--
   }
  }
 }') || die "GitHub returned an invalid release."
	valid_tag "$latest" || die "GitHub did not return an exact kit release tag."
	say "Installed kit: $current_tag"
	say "Latest kit release: $latest"
	case "$(release_order "$latest" "$current_tag")" in
	1) say "A newer kit release is available." ;;
	0) say "The installed kit is current." ;;
	-1) say "The installed kit is newer than the latest release." ;;
	esac
}

support_check() {
	if ! compat_json=$(curl -q -fsS --proto '=https' --max-time 20 --max-filesize 65536 \
		https://abyssreader.com/app/compat.json); then
		warn "Could not read the live app support schedule."
		return
	fi
	# Use the installed binary in an isolated, disposable container so the check
	# works even when the service is stopped. No server env or data is mounted.
	if ! printf '%s\n' "$compat_json" |
		docker run --rm -i --pull never --network none --read-only --cap-drop ALL --tmpfs /data \
			"$1" support-schedule --stdin 2>/dev/null; then
		warn "Could not interpret the live schedule with the installed image. See https://abyssreader.com/app/compat.json."
	fi
}

installed_image() {
	# An operator may have edited compose.yaml since the container was created.
	installed_cid=$(compose ps -aq server) || return 1
	if [ -n "$installed_cid" ]; then
		docker inspect -f '{{.Config.Image}}' "$installed_cid"
	else
		compose config --images server
	fi
}

installed_tag() {
	case "$1" in "$KIT_IMAGE_REPO":*) ;; *) die "the server must use a pinned kit image." ;; esac
	installed_ref=${1#"$KIT_IMAGE_REPO":}
	current_tag=${installed_ref%%@*}
	case "$installed_ref" in
	*'@'*) valid_digest "${installed_ref#*@}" || die "invalid installed kit image digest." ;;
	esac
	# The CI fixture uses a local image tag.
	[ "$current_tag" = 0.0.0-ci ] || valid_tag "$current_tag" || die "invalid installed kit tag."
}

upgrade_image() {
	if [ "$1" = "$KIT_VERSION" ]; then
		printf '%s\n' "$IMAGE"
		return
	fi
	if [ "$KIT_IMAGE_DIGEST" = local-test ]; then
		printf '%s:%s\n' "$KIT_IMAGE_REPO" "$1"
		return
	fi
	release_digest=$(curl -q -fsSL --proto '=https' --proto-redir '=https' --max-time 20 --max-filesize 128 \
		"https://github.com/IGlace/abyssreader-server-kit/releases/download/$1/image-digest.txt") ||
		die "Cannot download the image digest for kit $1 from GitHub. The kit repository must be public and that version must include image-digest.txt. Check that this machine can reach GitHub."
	valid_digest "$release_digest" || die "release $1 has an invalid image digest."
	printf '%s:%s@%s\n' "$KIT_IMAGE_REPO" "$1" "$release_digest"
}

schema_limit() {
	# Ask the actual prospective server binary, without mounting the live database.
	max_schema=$(docker run --rm --network none --read-only --cap-drop ALL --tmpfs /data "$1" maintenance schema-version) ||
		die "cannot determine the supported database schema for $1; operation refused."
	case "$max_schema" in '' | *[!0-9]*) die "invalid schema version reported by $1." ;; esac
}

require_clear_host() {
	[ ! -e "$PENDING" ] && [ ! -e "$ARMING" ] || die "An interrupted operation is pending. Run sudo sh install.sh recover."
}

operation_init() {
	require_clear_host
	[ -s "$STATE_DIR/install.info" ] && [ -f "$INSTALL_DIR/.env" ] || die "no installed server."
	CURRENT_IMAGE=$(installed_image)
	if [ -n "$CUSTOM_IMAGE" ]; then
		[ "$CURRENT_IMAGE" = "$CUSTOM_IMAGE" ] || die "the installed server image differs from KIT_IMAGE."
		[ "$COMMAND" != upgrade ] || die "upgrade a custom image using the fork-and-own-image guide."
	else
		installed_tag "$CURRENT_IMAGE"
	fi
	if [ "$COMMAND" = upgrade ]; then
		case "$(release_order "$target_tag" "$current_tag")" in
		-1) die "Refusing to downgrade $current_tag to $target_tag. Use install.sh rollback." ;;
		0)
			say "Already running $CURRENT_IMAGE."
			exit 0
			;;
		esac
		candidate_image=$(upgrade_image "$target_tag") || exit 1
	fi
	configured_image=$(compose config --images server)
	[ "$configured_image" = "$CURRENT_IMAGE" ] || die "compose.yaml and the installed container have different server image tags. To keep the installed version, set the server image in compose.yaml back to $CURRENT_IMAGE and retry. To change versions, then run install.sh upgrade."
	cid=$(compose ps -aq server)
	[ -n "$cid" ] || die "the installed server container is missing; run install first."
	DATA_VOLUME=$(docker inspect -f '{{range .Mounts}}{{if and (eq .Type "volume") (eq .Destination "/data")}}{{.Name}}{{end}}{{end}}' "$cid")
	[ -n "$DATA_VOLUME" ] || die "the server has no named /data volume."
	keep_env=$(docker inspect -f '{{range .Config.Env}}{{if eq (index (split . "=") 0) "ABYSS_BACKUP_KEEP"}}{{println .}}{{end}}{{end}}' "$cid")
	BACKUP_KEEP=${keep_env#ABYSS_BACKUP_KEEP=}
	pull_image "$IMAGE"
	remember_image "$IMAGE"
	helper ready </dev/null
	WORK_DIR=$(mktemp -d "$STATE_DIR/operation.XXXXXXXX")
}

# The host journal is independent of the service container (which Compose may remove).
# A power cut sees either the preceding record or the next complete one.
sync_state() {
	if [ "$(uname -s)" = Linux ]; then sync -f "$STATE_DIR"; else sync; fi
}
write_state() {
	printf '%s\n' "$2" >"$1.new" && sync_state && mv -f "$1.new" "$1" && sync_state
}
phase() { write_state "$WORK_DIR/phase" "$1"; }

operation_name() {
	case "$1" in *[!a-zA-Z0-9.]* | '') return 1 ;; operation.????????) return 0 ;; *) return 1 ;; esac
}

# Use the schema of the pinned image. Only old pins without a receipt need
# the locally cached image; never pull a retired tag to inspect an older pin.
cached_image_schema() {
    receipt=0
    case "$1" in "$KIT_IMAGE_REPO":*)
        if image_cached "$1"; then
            receipt=$(docker run --rm --pull never --network none --read-only --cap-drop ALL --tmpfs /data "$1" maintenance schema-version 2>/dev/null) || receipt=0
        fi ;;
    esac
    case "$receipt" in '' | *[!0-9]*) receipt=0 ;; esac
    printf '%s\n' "$receipt"
}

previous_pin_schema() {
	operation_name "$1" || return 1
	receipt=$(helper pin-schema "$1" </dev/null) || receipt=0
	case "$receipt" in '' | *[!0-9]*) return 1 ;; esac
	if [ "$receipt" != 0 ]; then
		printf '%s\n' "$receipt"
		return 0
	fi
	pin_image=$(cat "$STATE_DIR/$1/image" 2>/dev/null) || pin_image=""
	cached_image_schema "$pin_image"
}

begin_operation() {
	op=${WORK_DIR##*/}
	was_running=$(docker inspect -f '{{.State.Running}}' "$cid")
	if [ "$was_running" = false ]; then touch "$WORK_DIR/was-stopped"; fi
	cp "$INSTALL_DIR/compose.yaml" "$WORK_DIR/original.yaml"
	printf '%s\n' "$CURRENT_IMAGE" >"$WORK_DIR/image"
	printf '%s\n' "$DATA_VOLUME" >"$WORK_DIR/volume"
	printf '%s\n' "$IMAGE" >"$WORK_DIR/helper"
	printf '%s\n' "$COMMAND" >"$WORK_DIR/command"
	if ! was_edited compose.yaml; then touch "$WORK_DIR/managed"; fi
	if [ -f "$STATE_DIR/rollback-point" ]; then cp "$STATE_DIR/rollback-point" "$WORK_DIR/previous-point"; fi
	phase prepare
	JOURNALED=yes
	write_state "$ARMING" "$op"
	case "$COMMAND" in
	upgrade) operation_pin=$op ;;
	rollback) operation_pin=$point ;;
	*) operation_pin=$(cat "$WORK_DIR/previous-point" 2>/dev/null) || operation_pin=- ;;
	esac
	if [ "$operation_pin" != - ]; then operation_name "$operation_pin" || die "invalid rollback pin in installer metadata."; fi
    case "$COMMAND:$operation_pin" in
    uninstall:-) operation_schema=$(cached_image_schema "$CURRENT_IMAGE") ;;
	restore:operation.* | uninstall:operation.*) operation_schema=$(previous_pin_schema "$operation_pin") ;;
	*)
		if [ -z "${max_schema:-}" ]; then schema_limit "$CURRENT_IMAGE"; fi
		operation_schema=$max_schema
		;;
	esac
	helper begin "$COMMAND" "$operation_schema" "$operation_pin" </dev/null
	mv "$ARMING" "$PENDING" && sync_state
}

set_image() {
	awk -v old="$CURRENT_IMAGE" -v new="$1" '
 /^  [^ ]/{server=($0=="  server:")}
 server && $0=="    image: " old {$0="    image: " new; changed++}
 {print} END {if(changed!=1) exit 1}' "$INSTALL_DIR/compose.yaml" >"$WORK_DIR/next.yaml" ||
		die "cannot safely identify the pinned server image in compose.yaml."
	put "$WORK_DIR/next.yaml" compose.yaml 644
}

# Only this container creation receives the one-use boot permit. A service restarted by
# Docker during a swap lacks a usable permit and refuses to create or open a database.
probe_start() {
	permit=$(helper permit </dev/null) || return 1
	case "$permit" in '' | *[!0-9a-f]*) return 1 ;; esac
	printf 'services:\n  server:\n    environment:\n      ABYSS_RECOVERY_TOKEN: "%s"\n' "$permit" >"$WORK_DIR/boot.yaml" || return 1
	compose -f compose.yaml -f "$WORK_DIR/boot.yaml" up -d --force-recreate --remove-orphans || return 1
	wait_status
}

load_operation() {
	if [ ! -f "$PENDING" ] && [ -f "$ARMING" ]; then mv "$ARMING" "$PENDING" && sync_state || return 1; fi
	op=$(cat "$PENDING") || return 1
	operation_name "$op" || return 1
	WORK_DIR="$STATE_DIR/$op"
	DATA_VOLUME=$(cat "$WORK_DIR/volume") && IMAGE=$(cat "$WORK_DIR/helper") && CURRENT_IMAGE=$(cat "$WORK_DIR/image") &&
		ACTION=$(cat "$WORK_DIR/command") && saved_phase=$(cat "$WORK_DIR/phase")
}

# Delete only the pin and metadata belonging to a single installer-created operation.
drop_point() {
	operation_name "$1" || return 1
	helper unpin "$1" </dev/null || return 1
	rm -rf "${STATE_DIR:?}/${1:?}"
}

# This is also the power-loss path after a healthy or reverted status check. It is
# idempotent, including across power loss between removing the marker and the journal.
finish_operation() {
	outcome=$1
	helper finish </dev/null || return 1
	if [ "$ACTION" = upgrade ]; then
		if [ "$outcome" = healthy ]; then
			if [ -s "$WORK_DIR/previous-point" ]; then
				prev=$(cat "$WORK_DIR/previous-point") || return 1
				drop_point "$prev" || return 1
			fi
		else
			if [ -s "$WORK_DIR/previous-point" ]; then
				prev=$(cat "$WORK_DIR/previous-point") || return 1
				write_state "$STATE_DIR/rollback-point" "$prev" || return 1
			else
				rm -f "$STATE_DIR/rollback-point" || return 1
			fi
			helper unpin "$op" </dev/null || return 1
		fi
	elif [ "$ACTION" = rollback ] && [ "$outcome" = healthy ]; then
		prev=$(cat "$WORK_DIR/previous-point") || return 1
		rm -f "$STATE_DIR/rollback-point" || return 1
		drop_point "$prev" || return 1
	fi
	helper end </dev/null && helper ready </dev/null || return 1
	# A host reboot may have stopped even a previously verified service.
	if [ "$outcome" != reverted ] || [ ! -f "$WORK_DIR/was-stopped" ]; then
		if ! compose exec -T server abyssreader-server status >/dev/null 2>&1; then start || return 1; fi
	fi
	if [ -f "$WORK_DIR/managed" ]; then record compose.yaml || return 1; fi
	rm -f "$WORK_DIR/boot.yaml" || return 1
	rm -f "$PENDING" && sync_state || return 1
	JOURNALED=""
	if [ "$ACTION" != upgrade ] || [ "$outcome" != healthy ]; then rm -rf "$WORK_DIR"; fi
	WORK_DIR=""
}

# A lost installer folder can leave a perfectly good labelled data volume locked.
# Discover the exact Compose project and logical volume; never create or guess a volume.
recover_orphan() {
	orphan_project=${COMPOSE_PROJECT_NAME:-abyssreader-server}
	if [ -f "$INSTALL_DIR/compose.yaml" ]; then
		orphan_cid=$(compose ps -aq server 2>/dev/null) || orphan_cid=""
		if [ -n "$orphan_cid" ]; then
			orphan_project=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$orphan_cid") || die "cannot identify the installed Compose project."
		fi
	fi
	case "$orphan_project" in '' | *[!a-z0-9_-]*) die "invalid Compose project name." ;; esac
	volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$orphan_project" --filter "label=com.docker.compose.volume=data") || die "cannot list Docker volumes."
	if [ -z "$volumes" ]; then
		say "No labelled kit data volume was found for project $orphan_project. For a custom project, set COMPOSE_PROJECT_NAME and retry."
		[ -z "$ABANDON" ] && [ "${1:-}" != incomplete ] || return 1
		return 0
	fi
	case "$volumes" in *'
'*) die "multiple labelled data volumes match project $orphan_project; identify the original volume before recovery." ;; esac
	DATA_VOLUME=$volumes
	docker volume inspect "$DATA_VOLUME" >/dev/null || die "the data volume disappeared."
	pull_image "$IMAGE"
	say "Data volume: $DATA_VOLUME (Compose project $orphan_project)."
	status=$(helper orphan-status </dev/null) || die "cannot inspect the volume."
	say "$status"
	case "$status" in "No interrupted operation is pending."*)
		if [ "${1:-}" = incomplete ]; then
			helper describe-unlocked </dev/null || return 1
			[ -n "$ABANDON" ] || {
				say "The incomplete host journal still requires install.sh recover --abandon."
				return 1
			}
			say "The volume is unlocked. Type ABANDON to move the incomplete host journal aside:"
			IFS= read -r confirm && [ "$confirm" = ABANDON ] || die "abandon cancelled."
			helper audit-unlocked </dev/null || return 1
		fi
		return 0
		;;
	esac
	say "The host recovery journal is unavailable. Existing databases, images, backups and pins will be kept. A partial swap will return to its original file set, with displaced files retained on the volume."
	if [ -z "$ABANDON" ]; then
		say "Run sudo sh install.sh recover --abandon to verify the existing live database and release the boot interlock."
		return 1
	fi
	say "Type ABANDON to stop the server, preserve its data and release the boot interlock:"
	IFS= read -r confirm && [ "$confirm" = ABANDON ] || die "abandon cancelled."
	users=$(docker ps -q --filter "volume=$DATA_VOLUME") || die "cannot identify volume users."
	while IFS= read -r user; do
		[ -n "$user" ] || continue
		owner=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}/{{index .Config.Labels "com.docker.compose.service"}}' "$user") || die "cannot identify volume user."
		[ "$owner" = "$orphan_project/server" ] || die "volume $DATA_VOLUME is in use by another container ($user). Stop it before retrying."
	done <<EOF
$users
EOF
	while IFS= read -r user; do
		[ -n "$user" ] || continue
		docker stop "$user" >/dev/null || die "cannot stop the server using this volume."
	done <<EOF
$users
EOF
	users=$(docker ps -q --filter "volume=$DATA_VOLUME") || die "cannot recheck volume users."
	[ -z "$users" ] || die "a container still uses the volume; recovery paused."
	helper abandon </dev/null || die "volume remains locked; preserve it and inspect the reported recovery error."
	helper ready </dev/null || die "the data volume still has a pending recovery operation."
	say "The server is stopped. Reinstall or start a compatible kit image against volume $DATA_VOLUME."
}

recover_operation() {
	load_operation || return 1
	# A killed Docker CLI can leave its helper running in the daemon. Only this
	# operation owns this name, including when recovery itself was interrupted.
	docker wait "abyssreader-$op" >/dev/null 2>&1 || true
	if docker container inspect "abyssreader-$op" >/dev/null 2>&1; then
		docker rm "abyssreader-$op" >/dev/null 2>&1 || ! docker container inspect "abyssreader-$op" >/dev/null 2>&1 || return 1
	fi
	say "Recovering $ACTION (phase $saved_phase)."
	case "$saved_phase" in
	uninstalling)
		complete_uninstall
		return $?
		;;
	healthy | reverted)
		finish_operation "$saved_phase"
		return $?
		;;
	prepare)
		# The operator may have supplied an invalid archive; live data was never touched.
		phase reverted && finish_operation reverted
		return $?
		;;
	stopping | upgrade-start | swap | recovery-start) ;;
	*)
		warn "unknown recovery phase $saved_phase."
		return 1
		;;
	esac
	compose stop server || return 1
	cp "$WORK_DIR/original.yaml" "$WORK_DIR/old.yaml" && put "$WORK_DIR/old.yaml" compose.yaml 644 || return 1
	pull_image "$CURRENT_IMAGE" || return 1
	schema_limit "$CURRENT_IMAGE" || return 1
	case "$saved_phase" in
	upgrade-start)
		# A migration may have committed. Always replace the DB from the pre-upgrade pin.
		helper stage-pin "$op" "$max_schema" </dev/null && helper apply </dev/null || return 1
		;;
	swap) helper undo </dev/null || return 1 ;;
	esac
	if [ "$saved_phase" = stopping ] && [ -f "$WORK_DIR/was-stopped" ]; then
		# No swap or migration has started. A failed preservation attempt must
		# leave an already stopped installation usable for another restore.
		phase reverted && finish_operation reverted || return 1
		return 0
	fi
	if [ -f "$WORK_DIR/source-raw" ]; then
		# The original database was already damaged. After undo, keep its bytes
		# offline so the operator can immediately try another verified restore.
		touch "$WORK_DIR/was-stopped" || return 1
		phase reverted && finish_operation reverted || return 1
		say "Recovered the original damaged database files. The service is stopped; restore a verified archive."
		return 0
	fi
	helper check-live "$max_schema" </dev/null || return 1
	phase recovery-start || return 1
	if [ ! -f "$WORK_DIR/was-stopped" ]; then probe_start || return 1; fi
	phase reverted && finish_operation reverted || return 1
	say "Restored the previous image, data and service state."
}

do_upgrade() {
	schema_limit "$CURRENT_IMAGE"
	helper check-live "$max_schema" </dev/null
	helper preflight upgrade </dev/null
	begin_operation
	phase stopping
	compose stop server
	helper snapshot-pin "${BACKUP_KEEP:-14}" "$current_tag" "$op" </dev/null >/dev/null
	if [ "$KIT_IMAGE_DIGEST" = local-test ]; then
		docker pull "$candidate_image" >/dev/null || die "could not pull release $1."
	else
		pull_image "$candidate_image"
	fi
	remember_image "$candidate_image"
	# Publish the way back before the candidate can migrate anything.
	write_state "$STATE_DIR/rollback-point" "$op"
	phase upgrade-start
	set_image "$candidate_image"
	probe_start || die "the upgraded server failed its status check."
	phase healthy
	ACTION=upgrade
	finish_operation healthy
	say "Upgraded to $1. install.sh rollback restores the pre-upgrade backup."
}

preview_restore() {
	backup_source=$1
	shift
	if [ -n "$ALLOW_DEFINITION_ROLLBACK" ]; then set -- --allow-definition-rollback; fi
	case "$backup_source" in /data/backups/*/backup.json)
		gen=${backup_source#/data/backups/}
		helper preview-generation "${gen%/backup.json}" "$max_schema" "$@" </dev/null
		;;
	*) helper preview "$max_schema" "$@" <"$backup_source" ;;
	esac
}

do_restore() {
	schema_limit "$CURRENT_IMAGE"
	PREVIEW=$(preview_restore "$1")
	helper describe-preview "$PREVIEW" </dev/null
	say "Current writes made since this snapshot will be replaced."
	if [ -n "$ALLOW_DEFINITION_ROLLBACK" ]; then
		say "The live signing key stays in place. If the archive has a different signing state, republish definitions at versions above the prior floors."
	fi
	say "If a live image is missing or cannot be recovered, its path will be reported. The installer will keep an incomplete pre-restore snapshot and proceed with the confirmed restore."
	say "If the live database is damaged, a raw, unverified copy will be kept for manual salvage."
	if [ -z "$ASSUME_YES" ]; then
		say "Type RESTORE to replace the live data:"
		IFS= read -r confirm && [ "$confirm" = RESTORE ] || die "restore cancelled."
	fi
	begin_operation
	helper promote-preview "$PREVIEW" </dev/null
	PREVIEW=""
	preservation_preflight
	phase stopping
	compose stop server
	generation=$(helper snapshot-preserve "${BACKUP_KEEP:-14}" "$current_tag" </dev/null)
	say "Pre-restore snapshot: /data/backups/$generation/backup.json"
	kind=$(helper preservation-kind "$generation" </dev/null)
	if [ "$kind" = raw ]; then
		write_state "$WORK_DIR/source-raw" yes
		say "The pre-restore snapshot is raw, unverified. The selected restore archive has been verified."
		if [ -z "$ASSUME_YES" ]; then
			say "Type RESTORE again to continue with the verified archive:"
			IFS= read -r confirm && [ "$confirm" = RESTORE ] || die "restore cancelled; raw preservation files retained."
		fi
	fi
	phase swap
	helper apply </dev/null
	probe_start || die "the restored server failed its status check."
	phase healthy
	ACTION=restore
	finish_operation healthy
	say "Restored the verified backup. The server answers /api/status."
}

do_rollback() {
	point=$(cat "$STATE_DIR/rollback-point" 2>/dev/null) || die "no successful upgrade has a rollback point."
	operation_name "$point" || die "invalid rollback point."
	previous_image=$(cat "$STATE_DIR/$point/image")
	installed_tag "$previous_image"
	pull_image "$previous_image"
	schema_limit "$previous_image"
	say "Rollback to $previous_image loses writes made since the upgrade. Type ROLLBACK to continue:"
	IFS= read -r confirm && [ "$confirm" = ROLLBACK ] || die "rollback cancelled."
	begin_operation
	helper stage-pin "$point" "$max_schema" </dev/null
	phase stopping
	compose stop server
	phase swap
	helper apply </dev/null
	set_image "$previous_image"
	probe_start || die "the previous release failed its status check."
	phase healthy
	ACTION=rollback
	finish_operation healthy
	say "Rolled back to $previous_image."
}

# A tagged image can be shared with another stack. Never force-remove it.
remove_image_if_unused() {
	if docker image inspect "$1" >/dev/null 2>&1; then
		users=$(docker ps -aq --filter "ancestor=$1") || return 1
		if [ -n "$users" ]; then
			say "Kept image $1 (another container uses it)."
			return 0
		fi
		docker image rm "$1" || warn "Kept image $1 (Docker refused removal; it may be shared)."
	fi
}

complete_uninstall() {
	if [ -f "$WORK_DIR/delete-volumes" ]; then check_final_archives || return 1; fi
	# Safe to repeat after the operator's confirmed deletion, even after Docker has
	# already removed some resources. Never remount a volume that has been deleted.
	if docker volume inspect "$DATA_VOLUME" >/dev/null 2>&1; then
		helper finish </dev/null && helper end </dev/null || return 1
	fi
	compose down || return 1
	if [ -f "$WORK_DIR/delete-volumes" ]; then
		while IFS= read -r volume; do
			[ -n "$volume" ] || continue
			if docker volume inspect "$volume" >/dev/null 2>&1; then docker volume rm "$volume" || return 1; fi
		done <"$WORK_DIR/volumes"
	else
		say "Kept data volume $DATA_VOLUME."
	fi
	while IFS= read -r image_tag; do
		[ -z "$image_tag" ] || remove_image_if_unused "$image_tag"
	done <"$WORK_DIR/images"
	trash=$(mktemp -d "$INSTALL_DIR-uninstall.XXXXXXXX") || return 1
	mv "$INSTALL_DIR" "$trash/installation" || return 1
	WORK_DIR=""
	HAVE_LOCK=""
	JOURNALED=""
	rm -rf "$trash"
	say "Uninstalled $CURRENT_IMAGE."
}

preservation_preflight() {
	previous_state=$(helper preflight preserve </dev/null)
	if [ "$previous_state" = raw ]; then write_state "$WORK_DIR/source-raw" yes; fi
}

confirm_missing_images() {
	missing=$(helper deletion-guard "$1" </dev/null) || die "volume deletion refused by the final archive check."
	if [ -n "$missing" ]; then
		say "The verified final archive is incomplete. These images are unavailable in live data and local backups:"
		printf '%s\n' "$missing"
		say "Type DELETE INCOMPLETE to delete the volume with these images missing:"
		IFS= read -r confirm && [ "$confirm" = 'DELETE INCOMPLETE' ] || die "deletion cancelled; the volume and final archive are retained."
	fi
}

# Keep a fingerprint in the durable journal: a retry after a power loss must
# refuse deletion if a verified external archive has disappeared or changed.
remember_final_archive() {
	digest=$(sha256_of "$archive_dir/$1")
	case "$digest" in '' | *[!0-9a-f]*) die "cannot checksum $1." ;; esac
	[ "${#digest}" -eq 64 ] || die "incomplete checksum for $1."
	write_state "$WORK_DIR/$1.sha256" "$digest"
}
check_final_archives() {
	saved_dir=$(cat "$WORK_DIR/final-archive-dir") || return 1
	for archive_name in backup.tar full-volume.tar; do
		if [ "$archive_name" = full-volume.tar ] && [ ! -f "$WORK_DIR/raw-final" ]; then continue; fi
		expected=$(cat "$WORK_DIR/$archive_name.sha256") || return 1
		[ -n "$expected" ] && [ -f "$saved_dir/$archive_name" ] && [ ! -L "$saved_dir/$archive_name" ] &&
			[ "$(sha256_of "$saved_dir/$archive_name")" = "$expected" ] || {
			warn "Final archive $saved_dir/$archive_name is missing or changed; keeping the volumes."
			return 1
		}
	done
}

do_uninstall() {
	address=$(info_get PUBLIC_URL)
	[ -n "$address" ] || die "the installed server address is missing."
	project=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$cid")
	[ -n "$project" ] || die "cannot identify this installation's Compose project."
	printf '%s\n' "$DATA_VOLUME" >"$WORK_DIR/volumes"
	for label in caddy_data caddy_config; do
		docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.volume=$label" >>"$WORK_DIR/volumes"
	done
	{
		compose config --images
		printf '%s\n' "$IMAGE"
		if [ -f "$STATE_DIR/used-images" ]; then cat "$STATE_DIR/used-images"; fi
		if [ -s "$STATE_DIR/rollback-point" ]; then
			point=$(cat "$STATE_DIR/rollback-point")
			operation_name "$point" || die "invalid rollback point."
			cat "$STATE_DIR/$point/image"
			if [ -f "$STATE_DIR/$point/helper" ]; then cat "$STATE_DIR/$point/helper"; fi
		fi
	} >"$WORK_DIR/image-list"
	sort -u "$WORK_DIR/image-list" >"$WORK_DIR/images"
	say "Uninstall removes this stack's containers and network, $INSTALL_DIR (including .env, import/, settings and the pinned rollback point metadata)."
	say "These image tags will be removed when unused:"
	cat "$WORK_DIR/images"
	say "These volumes are deleted only with exact address confirmation; otherwise they and their contents, including backups and any pinned snapshot, stay:"
	cat "$WORK_DIR/volumes"
	say "Caddy volumes, when listed, include TLS certificates, private keys and the ACME account."
	say "Off-machine copies and final archives outside $INSTALL_DIR stay. Save a final backup outside the install folder? [Y/n]"
	IFS= read -r final || die "uninstall cancelled."
	case "$final" in '' | Y | y | yes) final=yes ;; n | N | no) final=no ;; *) die "uninstall cancelled." ;; esac
	say "If live images are damaged, a final archive preserves the database and remaining images. The installer reports the missing paths and the incomplete archive location before deletion."
	say "To DELETE the data volume, type $address exactly. Any other answer keeps the volumes:"
	IFS= read -r confirm || die "uninstall cancelled."
	if [ "$confirm" = "$address" ]; then
		touch "$WORK_DIR/delete-volumes"
		final=yes
	fi
	begin_operation
	if [ "$final" = yes ]; then preservation_preflight; fi
	phase stopping
	compose stop server
	if [ "$final" = yes ]; then
		archive_dir=$(mktemp -d "$INSTALL_DIR-final.XXXXXXXX")
		generation=$(helper snapshot-preserve "${BACKUP_KEEP:-14}" "$current_tag" </dev/null)
		helper export-preserved "$generation" </dev/null >"$archive_dir/backup.tar.partial"
		helper verify-preserved "$generation" <"$archive_dir/backup.tar.partial"
		archive_state=$(helper describe-generation "$generation" </dev/null)
		mv "$archive_dir/backup.tar.partial" "$archive_dir/backup.tar"
		if [ "$(uname -s)" = Linux ]; then sync -f "$archive_dir"; else sync; fi
		say "Final archive state: $archive_state"
		say "Final backup: $archive_dir/backup.tar"
		if [ -f "$WORK_DIR/delete-volumes" ]; then
			write_state "$WORK_DIR/final-archive-dir" "$archive_dir"
			remember_final_archive backup.tar
			kind=$(helper preservation-kind "$generation" </dev/null)
			if [ "$kind" = raw ]; then
				write_state "$WORK_DIR/source-raw" yes
				say "The database is raw, unverified. Saving every file on the stopped volume, including earlier generations and pins."
				helper export-volume </dev/null >"$archive_dir/full-volume.tar.partial"
				helper verify-volume <"$archive_dir/full-volume.tar.partial"
				mv "$archive_dir/full-volume.tar.partial" "$archive_dir/full-volume.tar"
				if [ "$(uname -s)" = Linux ]; then sync -f "$archive_dir"; else sync; fi
				remember_final_archive full-volume.tar
				write_state "$WORK_DIR/raw-final" yes
				say "Full-volume salvage archive: $archive_dir/full-volume.tar. Type DELETE RAW to delete the volume:"
				IFS= read -r confirm && [ "$confirm" = 'DELETE RAW' ] || die "deletion cancelled; the volume and archives are retained."
			else
				confirm_missing_images "$generation"
			fi
		fi
	fi
	phase uninstalling
	complete_uninstall
}

COMMAND=install
ANSWERS=""
ASSUME_YES=""
ALLOW_DEFINITION_ROLLBACK=""
case "${1:-}" in
upgrade | rollback | restore | uninstall | recover)
	COMMAND=$1
	shift
	case "$COMMAND" in
	upgrade)
		[ "$#" -le 1 ] || die "upgrade takes one version or --check."
		if [ "${1:-}" = --check ]; then
			release_check
			exit 0
		fi
		target_tag=${1-"$KIT_VERSION"}
		if [ "$#" = 0 ] && [ "$target_tag" = 0.0.0-ci ]; then :; else valid_tag "$target_tag" || die "specify an exact release like 1.0.0 or 1.0.0-rc.1."; fi
		;;
	restore)
		archive=""
		for arg do
			case "$arg" in
			--yes) ASSUME_YES=1 ;;
			--allow-definition-rollback) ALLOW_DEFINITION_ROLLBACK=1 ;;
			--*) die "unknown restore option: $arg" ;;
			*)
				[ -z "$archive" ] || die "restore needs one kit backup archive."
				archive=$arg
				;;
			esac
		done
		[ -n "$archive" ] || die "restore needs a kit backup archive."
		;;
	recover)
		if [ "$#" = 1 ] && [ "$1" = --abandon ]; then ABANDON=yes; elif [ "$#" != 0 ]; then die "use recover [--abandon]."; fi
		;;
	*) [ "$#" = 0 ] || die "$COMMAND takes no arguments." ;;
	esac
	check_root
	select_custom_image
	check_release_image
	command -v docker >/dev/null || die "Docker must be installed."
	check_docker
	[ "$COMMAND" = recover ] || [ -f "$STATE_DIR/install.info" ] || die "no installed server."
	lock
	if [ "$COMMAND" = recover ]; then
		if [ ! -f "$PENDING" ] && [ ! -f "$ARMING" ]; then
			recover_orphan || exit 1
			exit 0
		fi
		if ! (load_operation) >/dev/null 2>&1; then
			warn "the host recovery journal is incomplete."
			recover_orphan incomplete || exit 1
			if [ -n "$ABANDON" ]; then
				retained=$(mktemp -d "$STATE_DIR/abandoned.XXXXXXXX")
				for marker in "$PENDING" "$ARMING"; do [ ! -e "$marker" ] || mv "$marker" "$retained/"; done
				sync_state
				say "Incomplete host journal retained at $retained."
			fi
			require_clear_host
			exit 0
		fi
		[ -z "$ABANDON" ] || die "a readable host journal exists. Run install.sh recover to resume that operation."
		JOURNALED=yes
		RECOVERING=yes
		load_operation || die "cannot read pending recovery journal."
		report_phase=$saved_phase
		report_action=$ACTION
		if ! (recover_operation) >>"$STATE_DIR/recovery.log" 2>&1; then die "Recovery failed. See $STATE_DIR/recovery.log and retry install.sh recover."; fi
		require_clear_host
		# Recovery owns the operation folder, including any retained rollback point.
		WORK_DIR=""
		case "$report_phase" in
		uninstalling) say "Completed confirmed uninstall." ;;
		healthy) say "Completed the previously verified $report_action. See $STATE_DIR/recovery.log." ;;
		*) say "Recovered $report_action from phase $report_phase. Restored pre-operation data and image. See $STATE_DIR/recovery.log." ;;
		esac
		exit 0
	fi
	operation_init
	case "$COMMAND" in
	upgrade) do_upgrade "$target_tag" ;;
	rollback) do_rollback ;;
	restore) do_restore "$archive" ;;
	uninstall) do_uninstall ;;
	esac
	exit 0
	;;
esac
while [ $# -gt 0 ]; do
	case "$1" in
	install | configure) COMMAND=$1 ;;
	--answers)
		[ $# -ge 2 ] || die "--answers needs a file"
		ANSWERS=$2
		shift
		;;
	--answers=*) ANSWERS=${1#--answers=} ;;
	help | -h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
	shift
done

check_root
case "$ANSWERS" in '' | /* | ./*) ;; *) ANSWERS="./$ANSWERS" ;; esac
select_custom_image
check_release_image
require_clear_host
if [ -n "$ANSWERS" ]; then
	check_answers_file
elif [ ! -t 0 ]; then
	die "no terminal to ask the questions in: run install.sh in a terminal, or give it --answers FILE.
(Download install.sh first; piping it into sh is not supported.)"
elif [ ! -t 1 ]; then
	die "the questions need this terminal for their output too, and it goes to a pipe or a file (| tee?).
Run sudo sh install.sh without the pipe; to keep a log of it, use: script -q -c 'sudo sh install.sh' install.log"
fi
check_docker
lock
require_clear_host
if [ -s "$STATE_DIR/install.info" ] && [ -f "$INSTALL_DIR/compose.yaml" ]; then
	current_cid=$(compose ps -aq server)
	if [ -n "$current_cid" ]; then
		DATA_VOLUME=$(docker inspect -f '{{range .Mounts}}{{if and (eq .Type "volume") (eq .Destination "/data")}}{{.Name}}{{end}}{{end}}' "$current_cid")
		[ -n "$DATA_VOLUME" ] || die "missing named server volume"
		pull_image "$IMAGE"
		helper ready </dev/null
	fi
fi
case "$COMMAND" in
install) do_install ;;
configure) do_configure ;;
esac
