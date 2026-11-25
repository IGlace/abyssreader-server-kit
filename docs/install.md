<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Installing a server with install.sh

`install.sh` installs an AbyssReader server on a Linux machine (a VPS) with Docker, and changes its settings later. Each kit release has its own `install.sh` on its release page: it installs exactly that release using the published multi-architecture image digest (`ghcr.io/iglace/abyssreader-server-kit:<version>@sha256:<digest>`).

```sh
# Download install.sh from the release page, read it, then:
sudo sh install.sh                 # install: answer the questions
sudo sh install.sh configure       # change the settings, then restart
```

Piping the script into `sh` (`curl … | sh`) is not supported: download it, read it, run it. The questions need the terminal for their output too, so do not pipe it into `tee` either; to keep a log, run it under `script -q -c 'sudo sh install.sh' install.log`. Ctrl-C cancels the questions without changing anything.

## What it needs and what it contacts

- **Docker Engine with the Compose plugin, Compose 2.24.0 or later.** The script checks the version before it writes anything and refuses an older one, saying how to update. Docker's own packages (`get.docker.com`, `download.docker.com`) have a current Compose; on Ubuntu 24.04 with the distribution's `docker.io`, `apt-get install docker-compose-v2`; Debian 12's own `docker.io` has no Compose v2 package, so use Docker's packages there. (2.24.0 is a conservative floor: the oldest Compose whose env-file handling the setup is checked against. Docker's documentation pages for the older behaviour could not be checked when this was written.)
- When Docker is missing, the script says so and offers two ways: pick your host's Docker one-click image for a new machine, or let it download **Docker's own install script** from `https://get.docker.com` and run it. It never installs anything without your yes, and never without a terminal.
- **What is contacted, and when:**
  - `get.docker.com` and `download.docker.com`: only when Docker is missing and you said yes;
  - `ghcr.io` on install, upgrade or recovery of an evicted image, to download the exact server image;
  - `api.github.com` and `abyssreader.com/app/compat.json` only on an explicit `upgrade --check`;
  - in the Caddy mode, Docker Hub (the `caddy:2.11.9` image), Let's Encrypt (Caddy gets the certificate itself; when Let's Encrypt fails, Caddy's default is to try ZeroSSL as a second issuer) and your own address (`https://<your address>/api/status`, to check that it answers);
  - your DNS resolver, for the Caddy mode's DNS check.
- An answers file that other users can read is refused when it holds a secret (a DSN, or an access key other than `generate` or `new`).
- **The server itself contacts no one**, except the error-reporting DSN when you set one (only to send errors).

## The questions

The questions are asked by the server image itself (`abyssreader-server setup`), so they and their checks are the server's own. Enter takes the answer in brackets; on `configure` that is the current one.

1. **Public address**: `https://reader.example.org` (no path). It is what users type in the app.
2. **HTTPS**:
   - **Caddy** (default): a Caddy container gets a Let's Encrypt certificate by itself. The address must be a domain name without a port, its A/AAAA records must point at this machine, and ports 80 and 443 must be open. The setup checks the DNS first: it resolves the name and compares it with this machine's addresses. When the machine has no public address on its interfaces (behind NAT), it cannot tell, says so, and goes on.
   - **My own proxy**: the server listens on `127.0.0.1:<port>` only (default 8080); your nginx, Cloudflare Tunnel or other proxy handles HTTPS and forwards to it with `X-Forwarded-For`.
3. **Server name**: 1–80 characters, shown in the app.
4. **Operator details**: your name (or your team's) and a contact (`mailto:` or `https://`), both required; privacy, terms and copyright links, optional, `https://` only.
5. **Sign-up open?** Default yes.
6. **Access key?** Default none. When you want one, the setup makes a random one (43 characters). On `configure` you keep it, make a new one or remove it; it is never changed unless you ask.
7. **Optional features**, all off by default (this release: `hosted-chapters`, `reports` and `backup` (a reader’s account export and restore)).
8. **Error-reporting DSN** (optional): a GlitchTip or Sentry DSN, `https://KEY@host/PROJECT`. What you type is not shown.
9. **Backup schedule.** Daily (default), weekly or off. Local snapshots share the server disk. Copy an export off the machine to survive disk loss.
10. **Backups to keep.** From 1 to 1000, default 14. The setup asks this when the schedule is active.

The rate limits are not asked: they keep the kit's defaults, and you can change them in `.env`.

The kit groups real IPv6 client addresses by /64, whether it sees them directly or receives them in `X-Forwarded-For` from a trusted proxy. In Caddy mode on an IPv4-only Docker bridge, Docker's userland proxy can forward IPv6 connections as IPv4. When Caddy sees only the bridge gateway, clients on that path share one rate-limit budget. Use My own proxy with a proxy that listens on IPv6 and sets `X-Forwarded-For` from the real connection if you need separate /64 budgets. Set `ABYSS_TRUSTED_PROXIES` to the addresses or CIDRs of the proxies in that chain if they fall outside the kit's defaults. See [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/) and [Caddy reverse proxy defaults](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#defaults).

## What it writes

| Path | Mode | What |
|---|---|---|
| `/opt/abyssreader-server/` | 755, root | the install folder |
| `.env` | **600**, root | every setting, secrets included (access key, DSN) |
| `compose.yaml` | 644, root | the stack: the image pinned to the release digest, a read-only container with no capabilities, Docker log rotation (10 MB × 3) |
| `Caddyfile` | 644, root | Caddy mode only |
| `import/` | 755, root | import folders for `admin import /import/<folder>` |
| `.installer/` | 700, root | the installer's own state: the previous files (to go back to), the checksum of the `compose.yaml` it wrote |

The data lives in the Docker volume `abyssreader-server_data`, which uninstall deletes only after an exact address confirmation.

`.env` uses Docker Compose's env-file format. The setup writes every value in double quotes with `\\`, `\"`, `\$` and `\n` escaped, so names with spaces, quotes, `$` or backslashes reach the server exactly; line breaks and other control characters are refused in names. Two settings in it are the installer's own, and the server ignores them: `ABYSS_INSTALL_HTTPS` (`caddy` or `own-proxy`) and `ABYSS_INSTALL_PORT`.

Nothing secret is printed, put on a command line or left in your shell history: the answers go through the setup's terminal or a file. To read the access key (users type it when they add the server): `sudo grep '^ABYSS_ACCESS_KEY=' /opt/abyssreader-server/.env`.

If you use Gemini CLI, configure it to read `AGENTS.md` in the operator workspace.
For Aider, open the workspace with `--read AGENTS.md`.

## Running it again, and configure

- `sudo sh install.sh` on a machine where the server is installed changes nothing: it starts the server if it is stopped, waits for `/api/status` and prints the next steps. It never rewrites `.env` or `compose.yaml`.
- `sudo sh install.sh configure` asks again with the current answers filled in, keeps every setting it does not ask about (a changed rate limit, a setting of a later release) and every secret you do not change, then recreates the containers and waits for `/api/status`.
  - If you changed `compose.yaml` or the `Caddyfile` yourself since the installer wrote it, it asks before replacing (or, when you leave the Caddy mode, removing) it; on yes your file is kept as `compose.yaml.edited-<date-time>` (or `Caddyfile.edited-…`) next to it, and without a terminal it keeps yours and says so.
  - If you stop `configure` (Ctrl-C, a closed SSH session, SIGTERM) while the new settings are starting, it goes back to the previous ones the same way.
  - If the server does not answer with the new settings, the script **goes back to the previous files by itself**, restarts the server with them and tells you how to read the logs (`cd /opt/abyssreader-server && sudo docker compose logs --tail 100 server`).
  - Answers that do not pass the checks change nothing.
- A fresh install whose server does not start is stopped, with its files kept: fix what the log names with `sudo sh install.sh configure`.

## Scripted installs: the answers file

`--answers FILE` runs the setup without questions (for example the demo server):

```sh
sudo sh install.sh --answers ./abyss-answers.env             # install
sudo sh install.sh configure --answers ./abyss-answers.env   # change settings
```

The file has the `.env` format (`KEY=value` lines, `#` comments, quotes as in `.env`) and the same names as `.env`. Keep it chmod 600: it may hold secrets (the script warns when others can read it).

```sh
ABYSS_PUBLIC_URL=https://demo.example.org
ABYSS_INSTALL_HTTPS=own-proxy          # caddy (default) or own-proxy
ABYSS_INSTALL_PORT=8091                # own-proxy: the port on 127.0.0.1 (default 8080)
ABYSS_SERVER_NAME="AbyssReader Demo"
ABYSS_OPERATOR_NAME="Demo team"        # required
ABYSS_OPERATOR_CONTACT=mailto:demo@example.org   # required
ABYSS_OPERATOR_PRIVACY_URL=https://example.org/privacy
ABYSS_OPERATOR_TERMS_URL=
ABYSS_OPERATOR_COPYRIGHT_URL=
ABYSS_SIGN_UP_OPEN=yes                 # yes/no or true/false
ABYSS_ACCESS_KEY=                      # see below
ABYSS_FEATURES=hosted-chapters         # comma-separated
# ABYSS_ERROR_REPORTING_DSN=https://<key>@<host>/<project>
ABYSS_BACKUP_SCHEDULE=weekly           # daily, weekly or off
ABYSS_BACKUP_KEEP=12
# ABYSS_INSTALL_DNS_CHECK=skip         # Caddy mode: install even when the name does not point here yet
# Any setting of .env.example that the setup does not ask about may be set too, e.g.:
# ABYSS_SIGN_UPS_PER_HOUR=5
```

- A setting the file lists replaces the current one; an empty value clears an optional one. A setting the file does not list keeps its current value (configure) or its default (fresh install). A fresh install needs at least the public address, the server name and the operator's name and contact.
- `ABYSS_ACCESS_KEY`: empty = no access key; `generate` = make one when there is none and **keep the existing one otherwise** (so running the same file twice never changes it); `new` = always make a new one; anything else = that key (at least 16 printable characters, no spaces).
- A `$` followed by a name (`$NAME`, `${NAME}`) is a variable Docker Compose would replace, so it is refused, naming the setting: write `$$` for a literal `$`, or put the value in single quotes.
- The file is checked exactly like the questions. Every wrong setting is named, in the order of the questions, never with a secret's value, and nothing is changed. A name that is not a setting of this release (a typo) is refused, and so are `ABYSS_DATA_DIR` and `ABYSS_LISTEN`, which the stack depends on.
- With an answers file, `install.sh` asks nothing at all: Docker missing is an error, and a changed `compose.yaml` is kept.

## Reader backup restores behind a proxy

Reader library restores with the optional `backup` feature finish in the same request that uploads the file. Configure the proxy to stream uploads to the kit and allow the server's file limit (20 MiB by default, up to 128 MiB), plus 64 KiB for multipart framing. Streaming lets the kit refuse an upload before reading its body when its queue or byte pool is full. The client should honor the `Retry-After` on a `429` response before sending the file again.

Allow enough time on both the client and the proxy for the upload, up to one minute in the queue, and the restore itself. There is no intermediate response during processing. The kit allows 20 seconds between reads and at most 40 minutes for the upload. Processing gives each 500-record batch up to 10 seconds, with an overall allowance up to 74 minutes 20 seconds for the largest admitted backups. A response timeout around 100 seconds can cut off a large restore, as can the default 125-second read timeout for published applications behind Cloudflare Tunnel. See [Cloudflare connection limits](https://developers.cloudflare.com/fundamentals/reference/connection-limits/). Use a proxy path with a long enough timeout or restore smaller files. When a disconnect cancels processing, committed batches remain. A new upload of the same file checks the existing records and continues with the rest. Pull sync to see committed changes before retrying. A retry behind the same short timeout can stop again while checking a large committed prefix.

When reinstalling at the same public address, preserve the kit database if readers need their old hosted chapter marks. A backup with AbyssReader's extra source website field can match a hosted URL to unrelated rows on a replacement database that reused the URL.

## Error reporting

With `ABYSS_ERROR_REPORTING_DSN` set, the server sends what it logs at the error level (the message and the log line's fields, a panic's stack included) to that GlitchTip or Sentry project, with `sentry-go`. No request is ever attached, so no headers, bodies, query strings, cookies or client addresses; before each event leaves, the user and request parts, breadcrumbs and the host name are removed and every email address and IP address left in the text is replaced with `[email]` and `[ip]`.

To see that events arrive: `cd /opt/abyssreader-server && sudo docker compose exec server abyssreader-server admin errors test` sends one test event with an example email and address in it; in GlitchTip or Sentry they read `[email]` and `[ip]`.

## Release downloads and installer tests

Download `install.sh`, its checksum and `image-digest.txt` from the kit's
[public releases](https://github.com/IGlace/abyssreader-server-kit/releases).
Maintainers publish the release locally after the build and compatibility checks pass. Each installer pins the tested image digest. Release notes list the contract
versions served and upcoming app support dates.

- `install.sh` in the repo has `KIT_VERSION=unreleased`. To install a fork from source, use [a custom image](developers/own-image.md). `scripts/stamp-install.sh <version> sha256:<digest> [out]` writes the release copy (default `dist/install.sh`) with the published image digest and prints the installer checksum. The local publisher attaches the installer, its checksum and `image-digest.txt`. The release copy has **no test hooks**: the lines between `# BEGIN TEST HOOKS` and `# END TEST HOOKS` are removed, so `ABYSS_INSTALL_DIR`, `ABYSS_INSTALL_WAIT` and `ABYSS_INSTALL_TEST` mean nothing to it.
- One `install.sh` runs at a time (Linux `flock` from util-linux and the lock `.installer/lock`, with its process id). A lock whose process is gone (a closed SSH session, a killed run) is taken over with a message, and abandoned setup work is cleaned up. An interrupted data operation retains its journal for `install.sh recover`.
- `scripts/test-install.sh <version>` runs the installer end to end with real Docker in the own-proxy mode, against an image built locally with that tag. CI runs it as root on a throwaway runner; on a development machine `scripts/stamp-install.sh --keep-test-hooks <version>`, then `COMPOSE_PROJECT_NAME=<your own> ABYSS_INSTALL_TEST=1 sh scripts/test-install.sh <version>` runs it in a temporary folder without root.

## Backups and restore

The server checks every minute. On a fresh volume it waits one minute before attempting the first scheduled backup so you can start the initial import. On later starts it catches up immediately if due. Daily and weekly mean 24 hours or seven days after the last published generation, including one marked incomplete. There is no fixed time of day. A future timestamp after a clock change produces a warning and triggers a catch-up. The server's timer survives restarts by reading the generations on the volume. Imports, removals, exports and backups take the same admin lock. App writes can continue during the SQLite `VACUUM INTO` snapshot. The server logs the start, completion, contention and failures. After lock contention or an error that prevents publication it retries in a minute. An incomplete generation produces an error log naming the unavailable images and waits for the next daily or weekly interval.

Each `/data/backups/kit-<UTC timestamp>/` contains `backup.json`, `abyssreader.db`, `covers/` and `pages/`. The manifest records the creation time, kit version, highest applied migration, database checksum and image count. Files have mode 600 and directories mode 700. The kit verifies SQLite integrity and every referenced image before atomically publishing the generation. Before pruning a generation the kit checks its manifest, SQLite snapshot and directory contents against the image references. Unrecognized entries remain on disk. Retention runs after publication. Scratch directories that the kit can identify as its own become eligible for cleanup after 24 hours.

The kit looks for each referenced cover or page by its content digest. It tries local generations newest first, then retained rollback pins and the live store. A valid backup copy is hard-linked into the new generation. Live files are copied and hashed. A missing live cover can therefore be recovered even when the newest copy is corrupt. When no verified copy exists, the kit names the path in the log and manifest. It keeps one incomplete generation alongside the complete history. The next complete generation removes the incomplete one after checking its contents. Run `docker compose exec -T server abyssreader-server admin backup list` to see the state of every generation. The local `abyssreader-server status` also reports damage in the latest one.

**Disk space.** Allow one database-sized snapshot, space for new or repaired image files, and a 64 MiB reserve for a backup. Upgrade checks free space before stopping. It reserves three database sizes in total for the snapshot, migration and WAL growth, and the writable database staged during rollback. It also budgets new images and the 64 MiB reserve. A rollback pin hard-links the verified generation, including the database. Restoring from a pin copies the database to its own writable inode and links the images. Space held by a pin remains in use when its scheduled generation is pruned. Pulling a new server image also uses Docker's image storage. Restoring a portable tar needs space for its full extracted contents and the pre-restore snapshot. The installer bounds staging by available bytes, allows for filesystem overhead and caps the archive at one million file entries.

After restoring from a local generation or rollback pin, the live images are hard links to the files in that backup. A bad disk block can damage the live image and every local generation sharing its inode. A completed `admin backup export` copied to a different machine provides an independent copy.

**Copy a backup off the machine.** The volume and snapshots share the same disk. A snapshot protects against a bad upgrade. To recover from disk loss, keep a copy on a second machine. It contains every user's data, including sessions and password hashes. Use private encrypted storage and keep a protected copy of the installation settings separately.

```sh
sudo -s
cd /opt/abyssreader-server
umask 077
generation=$(docker compose exec -T server abyssreader-server admin backup now) &&
  docker compose exec -T server abyssreader-server admin backup export "$generation" > /srv/kit-backup.tar.partial &&
  mv /srv/kit-backup.tar.partial /srv/kit-backup.tar
# Example destination on a trusted backup host
scp -p /srv/kit-backup.tar backup-host:private/
```

To restore a portable archive, run `sudo sh install.sh restore /srv/kit-backup.tar`. For a local generation, pass `/data/backups/GENERATION/backup.json`. While the server is still running, the installer verifies the archive, reads its actual SQLite migration version, and checks compatibility with the installed image. The prompt shows the archive's UTC creation time and age alongside the live SQLite and WAL modification times. The service can restart and admin commands remain available while you decide. Type `RESTORE` to replace subsequent writes, or pass `restore --yes FILE` in a script. The installer stops the server, takes a normal pre-restore snapshot even when the schedule is off, and prints its path. If some live images are unavailable it names them, keeps an incomplete snapshot of everything else and continues the confirmed restore.

**Damaged database.** If SQLite itself is corrupt, the stopped installer preserves a private, byte-checked copy of the database, any WAL, SHM or rollback journal, and the live image files. Its manifest, listing and warning say **raw, unverified**. These files may help with manual salvage. Only a complete, verified kit generation can be restored automatically. The installer asks for `RESTORE` again when it must preserve raw data. With `--yes` it prints the warning and proceeds. A raw copy needs space for the live files and the 64 MiB reserve. It expires after the configured number of newer complete backups. When SQLite must replay an existing rollback journal, allow extra space for a private database and journal copy. A storage, access or write error stops the restore. The installer keeps the replaced files until the new service passes its status check. The per-account `.tachibk` export has a separate format.

## Upgrade, rollback and uninstall

- `sudo sh install.sh upgrade --check` uses curl to read the latest release over HTTPS from GitHub, compares it with the installed tag, and checks the [live app support schedule](guides/support-schedule.md). It sends no version, settings or user data.
- `sudo sh install.sh upgrade` uses the exact release stamped into the downloaded script. For a different release such as `upgrade 1.2.3` or `upgrade 1.2.3-rc.1`, the installer reads that release's `image-digest.txt` before stopping the server. Older tags are refused, including when you run an older script. After preflight, the installer stops the server, snapshots it, pulls the candidate image and publishes the pre-upgrade rollback point before starting the candidate. A failed status check restores the previous image reference and its pre-migration data.
- `sudo sh install.sh rollback` asks you to type `ROLLBACK`. This discards writes since the last successful upgrade. The installer pulls the pinned previous image again if necessary, checks the snapshot against that image's schema, and verifies the server after starting it. If that start fails, it restores the image and data that were serving before the rollback attempt. Scheduled retention is independent of the rollback pin. A later successful upgrade replaces the pin.
- `sudo sh install.sh uninstall` lists the affected containers, network, install folder (including `.env`, `import/` and installer state), image tags and volumes before asking for confirmation. It offers a private final archive in a new mode-700 directory alongside the install folder. Type the complete public address including `https://` to delete the server and Caddy volumes, including Caddy’s TLS private keys, certificates and ACME account. This choice requires a verified final archive. Before deleting, the installer checks whether any declared missing image is recoverable on the volume. A recoverable omission stops deletion. For images lost everywhere, it lists each path and requires a second confirmation, `DELETE INCOMPLETE`. With a corrupt database it also writes `full-volume.tar` alongside the raw `backup.tar`. This separate salvage archive preserves all regular files on the stopped volume, including older backups and pins, and is compared byte for byte against a second read of the volume. Hard links share space inside it. Allow room on the host for both archives. The installer then requires `DELETE RAW`. A failed verification or cancelled extra confirmation keeps the volume. Closing stdin cancels the prompts. The installer skips any image used by a running or stopped container and removes tags without force. An inactive Compose file with no containers can still refer to a removed tag and may need to pull it again. Off-machine copies stay. Secure the final archive after uninstall.

## Recover after an interruption

After a power failure, run `sudo sh install.sh recover` with the install folder and data volume in place. The installer and server check for pending work before starting or opening SQLite. Recovery takes the installer lock. A disconnect or signal triggers the same recovery with its output redirected to `/opt/abyssreader-server/.installer/recovery.log`. If a recovery fails, correct the reported problem and run the command again. Keep the journal and volume together.

If the installer folder or its journal has been lost, run the stamped script with `sudo sh install.sh recover`. It identifies the labelled data volume and reports the recorded operation, database and pinned-image schema versions, rollback pin, newest complete generation and the consequences of abandonment. Legacy empty markers can report unknown fields. It lists missing or damaged media before requesting confirmation. Inspection exits nonzero while the operation is pending. For a custom Compose project, pass its original `COMPOSE_PROJECT_NAME`. When the displayed volume is correct, run `sudo sh install.sh recover --abandon` and type `ABANDON`. The installer stops that project's server and checks for other running users of the volume. In a partial swap it puts the original live files back together. A completed swap keeps the current live database if it passes verification. If it fails and a saved predecessor exists, the installer tries the predecessor and keeps the damaged candidate in the retained transaction. If verification still fails, the volume remains locked. An unavailable image does not prevent abandonment. The database must pass SQLite integrity and kit schema checks before the volume can be unlocked. Each confirmed abandonment leaves a dated `abandon.json` in a private `/data/.kit-abandoned-*/` directory, even when no transaction files remain. The local `abyssreader-server status` command shows the latest note. Any staged and displaced files stay under the printed `/data/.kit-abandoned-…/transaction` path. Existing generations and pins remain on the volume. Reinstall with a compatible image and the original settings, then check `/api/status`. A readable host journal uses ordinary `recover`.

| Recorded phase | What recovery does |
|---|---|
| Preparing or stopping | Discards staged work. Keeps the original database and brings the original service back after an interrupted stop. |
| Starting an upgrade | Restores the published pre-upgrade pin and the original image, even if the candidate already migrated SQLite. |
| Swapping a restore or rollback | Undoes the swap, including a partially moved set of files, then starts the original image. |
| Starting the original service during recovery | Retries that start and checks status. |
| Healthy or reverted | Finishes cleanup for the previously verified result. |
| Confirmed uninstall | Finishes the removal using the recorded volume decision. |
