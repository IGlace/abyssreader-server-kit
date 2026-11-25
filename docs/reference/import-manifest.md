<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Import manifest

Use a folder that contains `manifest.json` and the files it references.
The [JSON Schema](../../schemas/import-manifest.schema.json) defines the
fields. The manifest format follows kit semver and carries its own
`manifestVersion`. A breaking manifest change needs a kit major release.

Assign each title a stable `ref`. Reimport to update the same title. A title
omitted from the next import stays in the catalog. Keep relative paths
inside the import folder and store the rights record for any hosted title.

- [Sample catalog](../../examples/manifests/sample-catalog/README.md)
- [Sample hosted title](../../examples/manifests/sample-hosted/README.md)
- [Importer commands](cli.md)
