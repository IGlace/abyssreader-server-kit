<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Moderation and feature data

Add `reports` to the comma-separated `ABYSS_FEATURES` setting and recreate the server container. Its entry in `/api/status` enables reporting in the client. A signed-in reader can file one open report for a visible title. The API returns 204 on submission and 409 if that reader already has an open report for the title, even if the reason changes. Another account can report the same title.

With `comments` or `collections` enabled, readers can report comments, replies, collections and collection comments. Those reports go to the same queue, even when title reporting is off. Reviewing reports and acting on them is the server operator's responsibility.

## Review reports in the shell

```sh
docker compose exec server abyssreader-server admin reports --page 2 --limit 50 --title lantern-keeper
docker compose exec server abyssreader-server admin reports close 17
docker compose exec server abyssreader-server admin reports close content:21
docker compose exec server abyssreader-server admin reports close-title 42
docker compose exec server abyssreader-server admin reports close --account 9
```

The list starts on page 1 with 50 reports. Set `--limit` from 1 to 100. Open reports come first, newest first within each state. Each page shows how many open reports it contains, the total open for that filter, and the next command if another page exists. Use `--title` with the manifest ref or title ID. For a numeric ref, use `--title ref:123`. The filter includes reports on the title and its comments.

Use the **report ID**, including its `content:` prefix when present, to close one report, the **title ID** with `close-title`, or the **reporter account ID** with `close --account`. The shell prints the number closed. Closing an individual ID that is missing or already closed exits with status 1. Closing a report lets its author submit another. Deleting an account removes its reports. Removing a title deletes its open and closed reports and prints their count.

A report takes one of `wrong_information`, `wrong_source`, `chapter_not_working` or `other`. Optional details may be null or 1–500 characters. An account has a budget of 10 submissions per minute, including rejected attempts. A 429 response includes the seconds to wait in `Retry-After`. The server accepts up to 100 open reports per account and 250 per title. Once a title has 250 open reports, a later reader submitting a new report receives `400 validation_failed` until an operator closes one. The account cap uses the same response. Contract v1 has no distinct error code for a full report queue. Each account retains up to 500 rows. At that ceiling a successful submission retires its oldest closed row.

## Remove content or suspend an account

Back up the server before removing content. Use the target ID in the report queue when removing a comment, reply, collection or collection comment.

```sh
docker compose exec server abyssreader-server admin remove comment 21
docker compose exec server abyssreader-server admin remove collection 15
docker compose exec server abyssreader-server admin suspend reader@example.org --reason "Repeated spam"
docker compose exec server abyssreader-server admin unsuspend reader@example.org
```

Removing a comment removes its replies, votes and reports. Removing a collection also removes its membership, comments, votes and reports. Suspension ends existing sessions and blocks sign-in while keeping the reader's data. Both account commands accept an email or numeric account ID. Lifting a suspension lets the reader sign in again with a new session. A separate administrative disable still applies.

## Switch off or purge a feature

Remove the feature from `ABYSS_FEATURES` and recreate the container with the same data volume. Its routes return `404 feature_disabled` and it disappears from status. Adding it back restores access to its data.

To permanently remove its stored data, run the command **in the reconfigured server container**. It reads the command’s own environment. Restart the server with the feature off before purging. The command checks the feature setting and takes the same admin lock as import and title removal.

```sh
docker compose exec server abyssreader-server admin purge reports
docker compose exec -T server abyssreader-server admin purge hosted-chapters --yes
```

At the interactive prompt, type the exact feature name. Use `--yes` to supply confirmation in a script. The command prints deletion counts.

| Feature | Data deleted |
|---|---|
| `reports` | Every open and closed title report. |
| `comments` | Title comments and their replies, votes and reports. |
| `collections` | Collections, membership, comments, replies, votes and reports. |
| `stats` | No stored aggregates. Statistics are computed on request. |
| `hosted-chapters` | The hosted source and its source titles (including rights and reading-mode metadata), chapters, chapter-page associations, page-file and slice metadata, and kit image and temporary files in the pages directory. |

The catalog keeps its title IDs, covers, reports and non-hosted sources after a hosted purge. The command lists the refs losing their last source. A title without a source stays hidden from the catalog, genre counts, reports and library lists with either feature setting. Reimport a source under the same ref to restore the title and its library entries. To permanently remove a retained title, use `admin titles remove <ref>`. Sync retains each reader's marks and positions, including their original chapter URLs. Reimporting hosted chapters assigns new IDs, so those old URLs can no longer open the chapter. If image cleanup is interrupted after the database commits, run the purge again with the feature off to finish cleanup.

Account backups are streamed. With `backup` switched off, `admin purge backup` reports `stored_backup_files=0`. Restored library and reading state belong to core sync.


## Rankings

Enabling `stats` makes rankings public. Anyone who can access the server can see the display name and avatar of each reader with points in the selected period. Readers can see only their own detailed statistics.

Rankings award one point for each distinct title read, current title vote, comment or collection created in the selected period. Monthly rankings use the current UTC calendar month. Scores refresh at most once per minute per period. Equal scores are ordered by account ID. Account deletion, suspension and disabling take effect on the next ranking request.
