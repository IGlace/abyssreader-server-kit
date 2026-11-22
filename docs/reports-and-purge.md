<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Title reports and feature data

Add `reports` to the comma-separated `ABYSS_FEATURES` setting and recreate the server container. Its entry in `/api/status` enables reporting in the client. A signed-in reader can file one open report for a visible title. The API returns 204 on submission and 409 if that reader already has an open report for the title, even if the reason changes. Another account can report the same title.

## Review reports in the shell

```sh
docker compose exec server abyssreader-server admin reports --page 2 --limit 50 --title lantern-keeper
docker compose exec server abyssreader-server admin reports close 17
docker compose exec server abyssreader-server admin reports close-title 42
docker compose exec server abyssreader-server admin reports close --account 9
```

The list starts on page 1 with 50 reports. Set `--limit` from 1 to 100. Open reports come first, newest first within each state. Each page shows how many open reports it contains, the total open for that filter, and the next command if another page exists. Use `--title` with the manifest ref or title ID. For a numeric ref, use `--title ref:123`.

Use the **report ID** to close one report, the **title ID** with `close-title`, or the **reporter account ID** with `close --account`. The shell prints the number closed. Closing an individual ID that is missing or already closed exits with status 1. Closing a report lets its author submit another. Deleting an account removes its reports. Removing a title deletes its open and closed reports and prints their count.

A report takes one of `wrong_information`, `wrong_source`, `chapter_not_working` or `other`. Optional details may be null or 1–500 characters. An account has a budget of 10 submissions per minute, including rejected attempts. A 429 response includes the seconds to wait in `Retry-After`. The server accepts up to 100 open reports per account and 250 per title. Once a title has 250 open reports, a later reader submitting a new report receives `400 validation_failed` until an operator closes one. The account cap uses the same response. Contract v1 has no distinct error code for a full report queue. Each account retains up to 500 rows. At that ceiling a successful submission retires its oldest closed row.

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
| `hosted-chapters` | The hosted source and its source titles (including rights and reading-mode metadata), chapters, chapter-page associations, page-file and slice metadata, and kit image and temporary files in the pages directory. |

The catalog keeps its title IDs, covers, reports and non-hosted sources after a hosted purge. The command lists the refs losing their last source. A title without a source stays hidden from the catalog, genre counts, reports and library lists with either feature setting. Reimport a source under the same ref to restore the title and its library entries. To permanently remove a retained title, use `admin titles remove <ref>`. Sync retains each reader's marks and positions, including their original chapter URLs. Reimporting hosted chapters assigns new IDs, so those old URLs can no longer open the chapter. If image cleanup is interrupted after the database commits, run the purge again with the feature off to finish cleanup.

Account backups are streamed. With `backup` switched off, `admin purge backup` reports `stored_backup_files=0`. Restored library and reading state belong to core sync.
