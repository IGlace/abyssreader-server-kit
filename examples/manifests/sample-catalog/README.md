<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Sample catalog

An invented catalog for trying the import: two made-up sources and six titles, five with a cover file. Nothing here names a real site, person or work.

```sh
cp -r examples/manifests/sample-catalog import/     # compose.yaml mounts ./import at /import, read-only
docker compose exec server abyssreader-server admin import /import/sample-catalog
```

The cover images in `covers/` were drawn for this example and are under the same licence as the kit's other files (PolyForm Noncommercial 1.0.0, Copyright 2026 Abyss Reader); image files carry no licence header of their own.

The titles have no readable source on a kit server without definitions: the app shows them in the catalog and search, but cannot open chapters.
