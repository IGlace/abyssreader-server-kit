# Sync contract fixtures

The wire contract of `POST /api/sync/push` and `GET /api/sync/pull`. The backend (S1) and the app (S2) both test against these files; neither keeps a private copy.

| Path | Content | Validated against (`sync.schema.json#/$defs/…`) |
|---|---|---|
| `sync.schema.json` | JSON Schema, draft 2020-12, `$id` `https://abyssreader.com/schemas/sync.schema.json` | — |
| `push/request-*.json` | valid push requests (`all-kinds`: one item per kind; `boundaries`: limits) | `pushRequest` |
| `push/response-all-statuses.json` | every status and every `rejected` reason | `pushResponse` |
| `pull/delta-page.json`, `pull/snapshot-*.json` | pull pages | `pullResponse` |
| `pull/full-resync-409.json` | the 409 body | `pullConflict` |
| `invalid/<name>.json` | `{name, description, expect, reason, item}`: an item the server must reject | `invalidCase` |
| `rules/<name>.json` | `{name, description, serverNow, userCounts?, stored, incoming, expected:{status, reason?, storedAfter}}` | `ruleCase` |
| `merge/<name>.json` | `{name, description, serverNow, loserId, winnerId, before, expected:{after, tombstones}, thenPush?:{incoming, expected:{status, reason?, storedAfter}}}` (`thenPush`: §6.3a, a push keyed by the loser after the merge) | `mergeCase` |

## Conventions

- **Timestamps** are ISO-8601 UTC with exactly three fractional digits and a literal `Z` (`2026-10-10T14:00:00.000Z`). Dart's `toIso8601String()` prints six digits when microseconds are set, so format explicitly (`invalid/timestamp-with-micros.json`). The earliest accepted value is `1970-01-01T00:00:00.000Z` (the epoch, which the server sends for legacy rows); an earlier `clientUpdatedAt` or `detectedAt` passes the pattern but the server rejects the item as `invalid` (`invalid/timestamp-before-epoch.json`, a semantic case). The app clamps such a value to the epoch before sending it.
- **Cursors are opaque strings.** The server currently encodes a delta cursor as `d.<seq>` and a snapshot cursor as `s.<snapshotStartSeq>.<kind>.<lastId>`; clients store and echo them and never parse them. The first pull omits `cursor` (a full snapshot).
- **Optional fields** may be absent or `null`; both mean "unknown". The server emits them as `null`.
- **A deleted record** (push item or pull change with `deleted: true`) carries `data: null`.
- **Push validation is two-level:** the envelope (`pushEnvelope`: an `items` array of 1–500 objects, else **400**), then each item alone against `pushItem` (else that item is `rejected: invalid`). One bad item never fails the batch. `invalid/*` with `expect: "semantic"` pass `pushItem` and must be rejected by the server with their `reason`.
- **Rule cases:**
  - `stored` / `storedAfter` are the record for `incoming.key` written as a pull change: `deleted: true` is a tombstone, `null` means nothing.
  - `serverNow` drives the clock clamp (> `serverNow` + 5 min → stored as `serverNow`; exactly + 5 min is kept).
  - `userCounts` gives the user's row count per kind before the push (the cap applies to a new key when the count is already ≥ the cap).
- **Merge cases:**
  - `before` / `expected.after` list the user's full synced state for the loser and winner titles (`expected.after`: rows only; a `before` entry with `deleted: true` is a tombstone the user already holds).
  - A loser row meets the winner's row **or tombstone** under §5: the newer `clientUpdatedAt` wins, so a winner tombstone newer than the loser's row leaves no row on the winner (`winner-tombstone-vs-loser-row`).
  - `expected.tombstones` are the tombstones the merge writes for the loser's keys, stamped `max(serverNow, row.clientUpdatedAt + 1 ms)`: server time, and strictly newer than the row it replaces, which a device holding that row would otherwise keep on a tie (every fixture's rows are older, so they show `serverNow`).
  - Re-pointed and merged rows keep the winning row's `clientUpdatedAt` (library flags: the newer of the two).
- **Names** are kebab-case and say the behaviour (`older-stale`, `tombstone-resurrection`). Both test suites assert the exact file set.

## How each side loads them

The official server's tests and the app's tests read the same pinned fixtures.
Update both fixture inventories when adding a case. The official server's tests
run the full schema check. The app's tests check the file set and that every
file parses.
