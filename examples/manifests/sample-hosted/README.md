<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Sample hosted title

An invented **hosted title** for trying hosted chapters (the optional feature `hosted-chapters`): "The Night Ferry", three chapters whose pages are files in this folder. Nothing here names a real site, person or work.

```sh
# .env: ABYSS_FEATURES=hosted-chapters, then restart the server
cp -r examples/manifests/sample-hosted import/      # compose.yaml mounts ./import at /import, read-only
docker compose exec server abyssreader-server admin import /import/sample-hosted
```

The import prints `Hosted chapters: 3 added` and `Page files: 4 stored`. Episode 2 is one 800 × 9000 px long strip: taller than a page may be (6000 px), so the import cuts it into two slices of 800 × 4500, and the app reads episode 2 as two pages. The title is readable in the app on its own: the server lists itself as the hosted source.

The pictures (`covers/`, `ch1/`, `ch2/`, `ch3/`) are drawn by [`make-images.go`](make-images.go) (`go run make-images.go` in this folder draws them again, byte for byte) and are under the same licence as the kit's other files (PolyForm Noncommercial 1.0.0, Copyright 2026 Abyss Reader); image files carry no licence header of their own. The `rights` in the manifest are a made-up example of how a real hosted title credits its licence.
