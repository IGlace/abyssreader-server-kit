<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# AbyssReader server kit

The **server kit** is the documented code and guides from which anyone can build and run a **community server** for the AbyssReader app: the catalog, accounts, library, sync and community features the app connects to. The app reads chapters from sources on the device; the kit never crawls sources.

The kit is **source-available** for noncommercial use only. See [`LICENSING.md`](LICENSING.md).

## What is here

| Path | What |
|---|---|
| `contract/` | The **server contract**: the public description of every endpoint a server must offer (core features) or may offer (optional features), as OpenAPI 3.1 with examples. Released under tags `contract-vX.Y` (contract version `MAJOR.MINOR`). The AbyssReader mono-repo keeps a pinned copy. |
| `cmd/abyssreader-server/` | The server and its admin commands (one Go program). |
| `internal/` | `config` (environment variables, the optional features), `setup` (the installer's questions and the files it writes), `errorreport` (Sentry-protocol error reporting and its scrubber), `store` (SQLite and the migrations), `accounts`, `password`, `catalog` (titles, sources, hosted chapters, the import and the search), `manifest` (reads an import manifest), `covers` (cover download, checks and storage), `pages` (page images of hosted chapters: checks, slicing, storage), `usersync` (the library and sync records), `api` (the HTTP routes), `contracttest` (holds the server's answers to `contract/` in tests). |
| `schemas/` | `import-manifest.schema.json`, the format `admin import` reads (embedded in the binary). |
| `examples/manifests/` | Example import folders; CI checks every `manifest.json` against the schema and imports `sample-catalog` and `sample-hosted` into the image. |
| `install.sh`, `docs/install.md` | The installer for a Linux server and its guide (`abyssreader-server setup` asks its questions). |
| `Dockerfile`, `compose.yaml`, `.env.example` | The **simple setup**: one container, everything in one `/data` volume. |
| `LICENSE`, `LICENSE-DOCS`, `NOTICE`, `LICENSING.md` | The licences and what they mean. |

## What the server offers today

Contract v1:

- `GET /api/status`: the server's name, whether an access key is required, whether sign-up is open, the **operator details** and the contract versions with their switched-on optional features. Always public.
- Accounts (core): sign-up (a working session at once, any valid email, no email sending), sign-in, `GET`/`PUT /api/v1/me`, password change (a new token; every other session ends) and account deletion (everything the account created goes with it).
- The **access key**, when the operator sets one: every route except `/api/status` needs `X-Access-Key` (`403 access_key_required` / `access_key_invalid`, never a sign-out).
- The catalog (core): `GET /api/v1/titles` (search and browse: 1-based pages, `q`, the genre, status, language, source and rating filters, and the sorts `latest`, `popular`, `trending`, `alphabetical`), a title by id and by slug, `GET /api/v1/genres` and `GET /api/v1/sources`. Covers are served from the server's own storage.
- Sources and definitions (core): the key and manifest lists start empty. Operators can publish their own signed definitions. The manifest supports `ETag` and `304`.
- **Library and sync** (core): `POST /api/v1/sync/push`, `GET /api/v1/sync/pull` and `GET /api/v1/me/library?list=favorite|planning|blacklist`; the caller's lists and vote in every title's `me`, and the search leaves out the caller's blacklist. See [Library and sync](#library-and-sync).
- **Hosted chapters** (optional feature `hosted-chapters`, off by default): the server lists itself as the hosted source, `GET /api/v1/source-titles/{id}/chapters` and `GET /api/v1/chapters/{id}/pages`, and serves the page images from its own storage. See [Hosted chapters](#hosted-chapters).
- `GET /healthz` for the container health check.

- Optional error reporting to a GlitchTip or Sentry DSN, without emails, IP addresses or request bodies (`ABYSS_ERROR_REPORTING_DSN`).

Operators can schedule and restore full-server snapshots and roll back a failed upgrade. See the [installer guide](docs/install.md#backups-and-restore).

## Optional features

`ABYSS_FEATURES` in `.env` lists the optional features you switch on, comma-separated (all are off by default); restart the server after changing it. `/api/status` lists the ones that are on. A feature that is off answers `404 feature_disabled` on every one of its routes and **keeps its data**: switching it on again brings everything back. The server refuses to start on a name it does not know or a feature this kit version does not have yet.

Enable `backup` to let signed-in readers download their library from `GET /api/v1/me/backup` as a gzip-compressed `.tachibk`. Readers restore a Mihon, old AbyssReader or server export by posting multipart field `file` to `/api/v1/me/backup/restore`. Files are processed in memory. Restored records are delivered by sync. The file contains private reading activity and is unencrypted.

**Transfer limits.** Set `ABYSS_ACCOUNT_BACKUP_MAX_MIB` to limit the uploaded file (default 20 MiB, range 10–128). Configure your reverse proxy to stream the request, allow another 64 KiB for multipart framing, and allow time for processing before the report. The upload deadline allows 0.5 Mbit/s for the configured maximum plus 20 seconds of grace, capped at 40 minutes. An export gets three minutes plus one second for every 62,500 compressed bytes flushed, up to 40 minutes in total, including database reads. Both directions have a 20-second idle timeout.

**Memory and concurrency.** Allow at least **1.5 GiB available to the kit process** with backup enabled at default settings, or **2 GiB** at the 128 MiB file setting. Large catalogs and other active features can need additional memory. All uploads share a pool sized for two full requests including framing (40.125 MiB by default, 256.125 MiB at the largest setting). One file at a time can be decoded and restored. Up to three fully received files wait in arrival order for up to one minute. The server refuses new uploads before reading the body when the queue or upload pool is full. Its limits include 128 MiB decompressed, 50,331,648 UTF-16 code units, a separate 144 MiB ceiling on retained UTF-8 text, 20,000 title entries and 100,000 retained chapters. At maximum settings, allow roughly 475 MiB for the upload pool and one decoded model together, before matching, SQLite and garbage collection. An account spends one of six hourly restore attempts on admission to the decoder, including an invalid file. Two exports may run server-wide. Each account may run one restore and one export concurrently.

**Source identity.** Community sources without a mapped Mihon ID use a stable ID based on their source slug. Each kit database has its own persistent hosted-source ID because its hosted title and chapter URLs contain local row numbers. Keeping the database when moving the server preserves that ID. A Mihon re-export can therefore identify the same source even after it drops the AbyssReader website field.

**Processing time.** Ordinary sync pushes can take a turn between 500-record restore batches. Each batch has ten seconds for validation, waiting and storage. The overall deadline allows 60 seconds for setup plus eleven seconds per possible batch, including work to verify records already present. The largest admitted file gets 74 minutes 20 seconds. A stable account and catalog, available capacity, and storage that completes within those time bounds allow 100,000 marks in one upload. Files with enough additional list, vote or position records to reach the 100,000-write allowance can take two uploads.

**Restore results.** A completed restore returns `200` with catalog matches, committed and skipped chapter marks, and up to 500 unmatched entries. A unique name match counts even when the entry has no list to add. After **any restore `503 unavailable`**, pull sync to receive committed changes, honor the integer-seconds `Retry-After` header, then offer to submit the same file again. Use the HTTP status and header even when the app translates the error code. Time and write cutoffs can be retried. A full account needs capacity and files above the chapter-retention limit need to be split. Rate and admission refusals return `429` with `Retry-After`.

Enable `reports` for title reports, `comments` for title discussions and votes,
`collections` for shared title lists and their discussions, and `stats` for
reading statistics and rankings. The operator reviews content reports and
handles removals and suspensions through the shell. [Moderation and purge](docs/reports-and-purge.md)

Enable `hosted-chapters` to serve chapters and page images from the kit.

## Try it with Docker

```sh
cp .env.example .env          # fill in ABYSS_SERVER_NAME, ABYSS_OPERATOR_NAME, ABYSS_OPERATOR_CONTACT, ABYSS_PUBLIC_URL
docker compose up -d --build
curl http://127.0.0.1:8080/api/status
```

The server listens on `127.0.0.1:8080` only. The app accepts only `https://` server addresses.

## Install on a server

On a Linux VPS, download `install.sh` from a kit release and run `sudo sh install.sh`: it checks Docker, asks the setup questions (inside the server image), writes `/opt/abyssreader-server/` (`compose.yaml` pinned to that release, `.env` chmod 600), sets up HTTPS with Caddy and Let's Encrypt or behind your own proxy, starts the server and waits for `/api/status`. `sudo sh install.sh configure` changes the settings later; `--answers FILE` runs it without questions. See [`docs/install.md`](docs/install.md).

The matching operator workspace links to the [operator guides](docs/guides/README.md),
[CLI and schemas](docs/reference/README.md), and [developer guides](docs/developers/README.md).

## Administration

There are no admin accounts and no admin routes: whoever has shell access to the host administers the server.

```sh
docker compose exec server abyssreader-server admin users reset-password reader@example.com
docker compose exec server abyssreader-server admin users disable reader@example.com
docker compose exec server abyssreader-server admin users enable reader@example.com
```

- `reset-password` prints a new **temporary** password once and signs every session of the account out. It works for **72 hours**; after that it answers like a wrong password. The user signs in with it and chooses their own password in the app, which ends the time limit. There is no email: pass the password on yourself.
- `disable` signs the account out everywhere; signing in then answers `403 account_disabled`. `enable` undoes it.

Use `abyssreader-server admin reports` to review the queue, page through older reports, filter by title, or close reports. To erase one feature's stored data, switch it off, restart the server and run `abyssreader-server admin purge <feature>`. The command asks you to type the feature name. [Report moderation and purge commands](docs/reports-and-purge.md)

## Filling the catalog

The kit never crawls sources. You fill the catalog with an **import folder**: a `manifest.json` that matches [`schemas/import-manifest.schema.json`](schemas/import-manifest.schema.json), plus any cover files it names. See [`examples/manifests/sample-catalog/`](examples/manifests/sample-catalog/).

```sh
mkdir -p import && cp -r examples/manifests/sample-catalog import/   # compose.yaml mounts ./import at /import
docker compose exec server abyssreader-server admin import /import/sample-catalog
docker compose exec server abyssreader-server admin titles remove unfinished-map
docker compose exec server abyssreader-server admin covers retry
```

- Every title has a `ref` you choose and keep. Importing again updates the title with that ref (same id, same source title ids); an entry that has not changed is left alone. Merging sources into one title is listing them under one ref: the kit never guesses.
- Add an optional `year` (an integer from 1 to 9999) for the title’s first publication. Leave it out or set it to `null` when unknown. The year is stored in the kit catalog only.
- An import **never deletes a title**. `admin titles remove <ref>` removes one, with its source titles and, when no other title uses it, its cover. Every reader's library and sync records on it become removals that their devices receive on the next sync (see [Library and sync](#library-and-sync)). Inside one title, the entry is the whole truth: other names, genres, authors and source titles it no longer lists are dropped. Hosted chapters are the exception: see [Hosted chapters](#hosted-chapters).
- A source title (source + url) belongs to one title. Sources are listed in the manifest's `sources` (slug, name, `website`, lang, optional icon URL and Mihon/Tachiyomi ids); a source imported earlier need not be listed again.
- The import prints what it added, updated and left unchanged, and every title it could not take with the reason (exit code 1); the other titles are imported.
- Titles are imported one by one, in the manifest's order. When two refs **swap slugs**, or a **source title moves from one ref to another**, the first of them may fail because the other still holds the slug or source title: import again and the second run takes it.
- A Mihon/Tachiyomi id is a positive signed 64-bit integer written as decimal text. The import strips leading zeroes and refuses duplicates or a claim already held by another source. To transfer an id, first reimport its old source without that id, then add it to the new source.

**Covers.** A title's cover is either `coverFile` (a JPEG, PNG or WebP file in the import folder) or `coverUrl`. The import stores every cover in the server's storage (`/data/covers/`, named by the SHA-256 of its bytes) and serves it at `ABYSS_PUBLIC_URL/covers/<name>`: absolute, loaded with no headers (no access key, no session), and never changing, so the app may cache it forever. A cover that cannot be stored leaves its title imported without a cover and is listed in the report; `admin covers retry` downloads the failed cover URLs again. There is no background job.

A cover URL is treated as hostile input. The download:
- is `https://` only, also on every redirect (at most 3);
- never reaches a loopback, private, link-local, carrier-grade NAT, multicast or other non-public address (checked on the address actually connected to), nor `localhost` or `.local` names, and ignores proxy settings;
- gives up after 10 s to connect, 10 s for TLS, 15 s for the answer's headers and 30 s in all;
- takes only a `200` answer with `Content-Type` `image/jpeg`, `image/png` or `image/webp`, at most 5 MiB;
- keeps the file only when its bytes are a still JPEG, PNG or WebP (no GIF, AVIF or animation) of at most 4000 × 6000 pixels. The image header gives the format and size, and an incomplete file is refused. The server checks the container without decoding pixels.

Covers download four at a time, and each one is stored as soon as it arrives: an interrupted import keeps the covers already linked to titles, and the next import or `admin covers retry` fetches only the missing ones. These commands also clean up unreferenced cover images and incomplete `.incoming-*` files under the admin lock.

**Views.** `GET /api/v1/titles/{id}` and `/by-slug/{slug}` count a view (the `trending` sort) unless `countView=false`, at most once per client and title per hour, and only on `GET`.

Cover files from the import folder pass the same image checks. They must be plain files inside the folder.

## Hosted chapters

With `ABYSS_FEATURES=hosted-chapters` the server stores chapters itself and the app reads them from it, with no definition. Import them like the catalog, from an import folder: a title with `hosted` instead of (or beside) `sourceTitles`. See [`examples/manifests/sample-hosted/`](examples/manifests/sample-hosted/) and the schema.

- The server lists itself in `GET /api/v1/sources` as **one** source, slug `hosted`, `hosted: true`, with the server's name and its own address (`ABYSS_PUBLIC_URL`) as `website`. Its lang is the one language of its titles, or `mul` when they have several. The slug `hosted` is reserved: a manifest cannot use it for a source of its own.
- Each entry of a title's `hosted` is one **hosted source title**, one per language (`lang` is its key). It comes first in the title's `sources`, with the url `/source-titles/{id}`, `hasDefinition: false`, the optional `readingMode` hint (`paged_ltr`, `paged_rtl`, `long_strip`) and the optional `rights` (holder, licence, licenceUrl, attribution, changes; the app shows them as they are). Changing a hosted source title's `lang` makes a new one: its chapters get new ids.
- Each chapter has a `ref` you choose and keep: importing again updates the chapter and keeps its id (and so its url `/chapters/{id}`, which the app's reading marks and sync rely on). Optional: `number`, `name`, `volume`, `credits` (who made or translated it), `publishedAt` (a date or a UTC time; by default the time of the import that first added it). Chapters are listed by number (`order` `desc` by default; chapters without a number last); chapters with the same number keep the manifest's order in both orders.
- `pages` are files in the import folder, in reading order, at most 500, as a path or `{file, width, height}` (give one side or both; each given side must be the file's own). They must be plain files: a link, folder, FIFO or device is refused, and a path may not lead out of the folder. Nothing is downloaded.

**What an import changes.** A title entry **with** `hosted` is the whole truth for that title's hosted chapters, but the import never removes a chapter on its own: a chapter (or a whole language) the entry no longer lists is **kept** and listed in the report, and the exit code is 1, until you import again with `admin import --remove-chapters <folder>`. A removed chapter loses its id, so readers lose their marks on it and an added-back chapter is a new one; the report names every chapter it removes. A title entry **without** `hosted` leaves the title's hosted chapters as they are, so a catalog-only folder that lists the same ref never touches hosted content. Two admin commands that change the catalog (`import`, `titles remove`, `covers retry`) never run at once: the second one stops with a message (the lock is `/data/admin.lock`).

**Page images** are still JPEG, PNG or WebP (no GIF, AVIF or animation), at most **2000 px wide, 6000 px tall and 5 MiB** each. The import reads every page file whole and checks it:
- wider than 2000 px, larger than 5 MiB (while at most 6000 px tall), more than 100 times as tall as it is wide, not an image, animated, damaged anywhere (every file is decoded once), carrying an EXIF rotation (save it upright first, so that the width and height are what every viewer shows), or a declared side that is not the file's own: the chapter is not imported. One report line names the chapter, the page and the reason, and the exit code is 1. A chapter imported before keeps its pages, and the other chapters are imported.
- a chapter that would be served as more than **500 images** (its pages and the slices of its strips) is refused the same way: split it.
- **taller than 6000 px** (a long strip): cut into slices of even height, each at most 6000 px tall, from top to bottom. A file is at most 60 000 px tall (10 slices). A JPEG or lossy WebP strip gives JPEG slices (quality 90); a PNG, a lossless WebP or a strip with transparency gives PNG slices. A slice over 5 MiB refuses the page, and the slices already stored for it are removed.
- Decoding takes memory, so the import estimates it from the file's header before decoding, per decoder, and refuses a file that would take more than **128 MiB**. For example, a colour baseline JPEG takes 3 bytes a pixel and a progressive one 15; CMYK takes 8; an interlaced PNG takes half as much again as a plain one; a lossless WebP takes 8. A 2000 × 16 000 colour strip fits as a baseline JPEG, not as a progressive one. A page file is at most 50 MiB.

What is stored carries **no metadata**: EXIF (GPS included), XMP, IPTC, comments and PNG text chunks are removed without touching the image data (a JPEG's colour profile and Adobe transform stay). Every page image is stored in `/data/pages/`, named by the SHA-256 of its bytes, and served at `ABYSS_PUBLIC_URL/pages/<name>`: absolute, loaded with no headers (no access key, no session), `Cache-Control: public, max-age=31536000, immutable`. A name always means the same bytes, so a replaced page file gets a new URL and the other pages keep theirs. Page files are stored one by one as they are read: an interrupted import keeps what it stored, and the next import reads only what is missing. A page file stored before (the same bytes) is used again without being read again. At the end of every import and every `admin titles remove`, the page images no chapter uses are deleted, and so is every file in `/data/pages/` with a stored image's name that no record names (left by a stopped import). Other files there, and the covers, are never touched.

**Rate limits.** The two hosted routes and the page images each have a budget per client per minute (`ABYSS_HOSTED_REQUESTS_PER_MINUTE`, default 600, and `ABYSS_PAGE_IMAGES_PER_MINUTE`, default 1800); past it they answer `429 rate_limited` with `Retry-After`, and the app waits that long. Behind a proxy or CDN, the client is the address your trusted proxy passes in `X-Forwarded-For`. The page list of a chapter is never longer than 500 images.

With the feature off, the two routes and the page images answer `404 feature_disabled`, and the hosted source and its source titles are left out of `/sources`, the titles and the `source` and `lang` filters. A title whose only source titles are hosted is left out everywhere (browse, search, totals, by id and by slug, genre counts). Nothing is deleted, and switching the feature on again brings it all back.

## Library and sync

The app keeps a signed-in user's library and reading state on the phone and syncs it with the server: the library lists (favorite, planning, blacklist), the vote on a title, the reading position in each title, the read and bookmark marks of each chapter, and the update inbox. The server follows the sync contract (`contract/sync/sync.schema.json` and its rules) exactly as the official server does, and its fixtures in `contract/sync/` are among the kit's tests.

- **Push** takes 1 to 500 changes (a body of at most 1 MiB); a request outside that is `400 validation_failed`. Each change is judged on its own: `applied`, `stale` (the server holds a newer one: the later `clientUpdatedAt` wins; on a tie the stored one stays, except a position with a higher chapter, then page), or `rejected` with `invalid` (it breaks the schema), `unknown_title`, `unknown_source` or `cap`. A time more than 5 minutes in the future counts as now.
- **Caps** per account: 100 000 chapter marks, 20 000 of every other kind, counting only new records (changing one never counts). And a ceiling of **250 000 stored rows** in all, removals included: a removal of something the server never had is stored too (the contract says so), so the ceiling keeps one account from growing the database without end. The caps add up to 220 000; the rest is room for 90 days of removals, far more than a real reader makes. A change that would add a row past the ceiling is `rejected: cap`; an update, a removal of a stored record and a re-add always pass.
- **Pull** without a cursor gives every record (in pages of `limit`, default 500, at most 1000, with `hasMore`); later pulls with the cursor give what changed since, removals included. A cursor is a string the server makes (`d.<number>` or `s.<number>.<number>`) and the app only echoes; one of another form is `400`. A cursor is not tied to an account: it counts that account's own changes only, so a cursor from another account reads nothing of anyone else. A well-formed cursor the server cannot serve is `409`, and the app pulls everything again: one older than a removal that was cleared (removals are cleared after **90 days**, every hour, a few hundred per transaction so that pushes never wait long), or one ahead of the account's last change (for example after the server was restored from an older backup).
- **Two devices at once:** every push is one write transaction, and SQLite lets one writer in at a time (the admin commands too), so nothing is lost and every change gets its own number.
- **Rate limits** per account per minute: `ABYSS_SYNC_PUSHES_PER_MINUTE` (default 120) and `ABYSS_SYNC_PULLS_PER_MINUTE` (default 300); past them the server answers `429 rate_limited` with `Retry-After`.
- **Hidden titles.** While `hosted-chapters` is off, a title with only hosted source titles is hidden, but its records stay: a pull still gives them, a push to it is still applied (chapter marks on the hosted source too), and `/me/library` leaves it out until the feature is on again.
- **Removed titles.** `admin titles remove` turns every record on the title into a removal in the same transaction; a later push naming it is `unknown_title`. A title's id is never used again. Hosted chapters removed with `--remove-chapters` keep the marks that point at them; the app no longer lists those chapters.
- **Account deletion** removes the account's records and removals with it.

## Limits

- Sign-ups per client per hour, password checks per client per minute, and wrong passwords per account per 15 minutes, all set in `.env`. A client is an IPv4 address or an IPv6 /64; behind a proxy it is the `X-Forwarded-For` address the proxy adds (see `ABYSS_TRUSTED_PROXIES` in `.env.example`).
- At most 2–4 password hashes run at once (Argon2id, 19 MiB each), so a burst of sign-ins cannot exhaust a small machine's memory. When every slot stays taken for 3 seconds the server answers `503 unavailable` with `Retry-After`.

## Settings

Environment variables are the only way to configure the server. Every one is listed, with its default, in [`.env.example`](.env.example). The server refuses to start, naming each wrong setting, when one is missing or invalid.

## Development

See [Contributing](CONTRIBUTING.md) for the Go and sqlc checks and the
[developer guides](docs/developers/README.md) for building a fork.

Migrations (goose, embedded) run forward only at start-up. Every table that holds something an account created references `users(id) ON DELETE CASCADE`; a test enforces it, so account deletion keeps removing everything.

CI (`.github/workflows/ci.yml`, GitHub-hosted runners) runs the tests (example manifests against the schema included), `sqlc diff`, the licence-header check and the third-party licence gate, and builds and starts the image and imports the sample catalog into it.

The image carries `LICENSE`, `LICENSE-DOCS`, `NOTICE` and `THIRD_PARTY_NOTICES` in `/usr/share/doc/abyssreader-server/`. `THIRD_PARTY_NOTICES` is generated at build time by `scripts/notices` from the modules linked into the binary, and the build fails on a GPL, LGPL, AGPL or SSPL module.

## Status

The kit repository is private during development. The owner makes it public before pushing the first release tag so operators can download the release files.
