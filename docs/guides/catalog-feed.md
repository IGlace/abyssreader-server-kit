<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Feed your catalog

Each server is run by its server operator, who is responsible for what the server offers and for the data you send it.
Publish a copyright contact in your operator details. Only use a feed you
have permission to read and share.

The kit's [Ink Harbor example](../../examples/catalog-feed/README.md) reads an
invented site. Start a local kit once to initialize its data folder. From the
kit source tree, serve the example site's files on loopback in one terminal:

```sh
python3 -m http.server 8095 --bind 127.0.0.1 --directory examples/catalog-feed/sample-site
```

In another, set `ABYSS_DATA_DIR` to that kit's data directory and
`ABYSS_PUBLIC_URL` to its configured address. Build its CLI and run the feed:

```sh
go build -o bin/abyssreader-server ./cmd/abyssreader-server
go run ./examples/catalog-feed -kit ./bin/abyssreader-server
```

It writes `import/catalog-feed/manifest.json` and then invokes the importer.
Run it twice. The second run should report two unchanged titles. For a kit
in Docker, run the importer in the server container and put the output
directory under the mounted import folder.

When adapting the example, retain a stable ref for each title and review the
[manifest format](../reference/import-manifest.md). First run it by hand and
inspect the import summary. Then schedule the feed with host cron or a
systemd timer, using absolute paths, your kit's environment, and a log the
operator can read. Schedule it on the host and keep the normal importer lock.
