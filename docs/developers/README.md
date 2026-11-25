<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Developer guides

The Go binary serves the HTTP API and the shell-only commands. `internal/api`
binds the OpenAPI routes to services, `internal/store` holds SQLite and
migrations, and catalog imports validate through `internal/manifest`. The
simple setup uses one Docker volume for SQLite, images and backups. The
[contract](../../contract/README.md) specifies what the app can rely on.

Run the [build, OpenAPI and test checks](../../CONTRIBUTING.md) from the kit
source tree. See [Fork and use your own image](own-image.md) for a local install.
