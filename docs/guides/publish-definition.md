<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Publish a source definition

Abyss Reader RE: is a reading tool. It has no manga of its own: it shows what the server you choose offers.
Each server is run by its server operator, who is responsible for what the server offers and for the data you send it.
The app checks that a server works as the server contract says. It does not check whether the content is lawful, and it cannot.

Publish a copyright contact in your operator details. List content only when its rights are clear.

## Author and publish

Definitions instruct the app how to read from a source. The app uses declared
HTTPS hosts, verifies each redirect, refuses local or private addresses, and
limits requests to about four per second per host. JavaScript steps are
unavailable. The kit validates definitions and serves the signed files.

Start from the [invented family and source](../../examples/definitions/). Read the
[definition schema](../../contract/definition.schema.json) and
[merge rules](../../contract/definition-merge.md). Pick a source slug that matches
the slug in your [catalog import](../reference/import-manifest.md). Copy your
family and source files to the server's mounted import directory, then run the
following from the kit's Compose directory. The first publication for a slug
can use version 1. Use a strictly higher version for each later publication or
revocation.

```sh
docker compose exec -T server abyssreader-server definitions resolve /import/family.json /import/source.json /tmp/resolved-v1.json
docker compose exec -T server abyssreader-server definitions lint /tmp/resolved-v1.json
docker compose exec -T server abyssreader-server definitions sign 1 /tmp/resolved-v1.json /tmp/publish-v1.json
docker compose exec -T server abyssreader-server definitions publish /tmp/publish-v1.json
```

In the app, add the server and open a chapter from one of that source's imported
titles. Switch to a different server and verify that the definition is scoped
to the server you added. Use `abyssreader-server definitions health` to inspect
anonymous error totals per source. Error kinds added by a newer app are
counted under `other`.

## Back up, revoke, and rotate

The kit creates the private signing key within `/data/secrets/` with
mode 0600 at first startup. Keep its file private. CLI operations display the
signing key fingerprint. Full kit backups include the key file and its
checksum, so keep those backups in encrypted, restricted storage. Losing the
key can require readers to trust a replacement.

Restore checks the archive's signing key and publication versions against the
live server. A restore to an existing server keeps its signing key file.
If you deliberately restore an archive with a different signing identity or
older offers, use `install.sh restore --allow-definition-rollback BACKUP.tar`
after taking a fresh backup. The kit clears the archived offers and their
health totals and keeps the live public key when restoring a database with
definition tables.
Backups from before definition support start with an empty publication registry.
Republish using versions higher than any previously published version for each
slug, including revocations. An empty replacement volume can recover the
archive's key.

Take a [full backup](../install.md#backups-and-restore) before revoking or
rotating. To revoke a source, choose a higher version than its current
publication. Republishing later requires a version above that revocation.

```sh
docker compose exec -T server abyssreader-server definitions revoke paper-lantern 2
```

Rotate an on-server signing key with the command below. It endorses the new key
with the current one and republishes all manifest entries at their next
versions. Keep time between rotations for readers to receive the endorsement
from the key they have pinned. Check the app before beginning another rotation.

```sh
docker compose exec -T server abyssreader-server definitions rotate
```

## Offline signing

Run the same kit CLI on a trusted offline machine. Create a signing file
with `abyssreader-server definitions keygen` and a path on that machine.
Back up the key securely there. Use the offline key when signing the resolved
JSON.

```sh
abyssreader-server definitions keygen private-offline.key
abyssreader-server definitions sign --key private-offline.key 1 resolved-v1.json publish-v1.json
```

Transfer only the signed bundle to the server. On its first publication,
record its complete `keyId` from the bundle on the trusted offline machine.
Use all 32 lowercase hexadecimal characters to approve the key on the server.
The short signing fingerprint is for display.

```sh
docker compose exec -T server abyssreader-server definitions publish --trust-key FULL_KEY_ID /import/publish-v1.json
```

Substitute the recorded ID for `FULL_KEY_ID`. Later bundles must match the
server's active key. To revoke offline, supply the key, slug, next version
and output path to `abyssreader-server definitions revoke`, then transfer and
publish the signed revocation. The bundled signing key stays on your offline
machine and its backup. The automatic rotation command uses the on-server key.
