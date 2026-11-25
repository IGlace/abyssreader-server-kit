<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# CLI reference

Generated from each command's help with `scripts/operator-docs.py`.

## abyssreader-server

```text
Usage:
  abyssreader-server [serve]                           run the server
  abyssreader-server admin reports [--page N] [--limit N] [--title REF|ID]
                                                       list reports, open first (default 50, max 100)
  abyssreader-server admin reports close ID            close one open report (content reports use content:ID)
  abyssreader-server admin reports close-title ID      close every open report for a title
  abyssreader-server admin reports close --account ID  close every open report by an account
  abyssreader-server admin remove comment|collection ID
                                                       remove content and its replies or comments
  abyssreader-server admin suspend ACCOUNT [--reason TEXT]
                                                       end sessions and suspend sign-in (account ID or email)
  abyssreader-server admin unsuspend ACCOUNT          allow sign-in again (account ID or email)
  abyssreader-server admin purge FEATURE [--yes]       delete a switched-off feature's data
  abyssreader-server doctor [--expected-ip IP]         check DNS, TLS, status, images, disk and backups
  abyssreader-server support-schedule --stdin          report app support dates from a compat.json on stdin
  abyssreader-server definitions keygen [FILE]           create a private signing key
  abyssreader-server definitions resolve FAMILY SOURCE OUTPUT
                                                         merge a family and source into a definition
  abyssreader-server definitions lint DEFINITION          check a resolved definition
  abyssreader-server definitions sign [--key FILE] VERSION DEFINITION OUTPUT
                                                         create a signed publication bundle
  abyssreader-server definitions publish [--trust-key KEY_ID] BUNDLE
                                                         publish a signed bundle to this server
  abyssreader-server definitions revoke SLUG VERSION      publish a signed revocation
  abyssreader-server definitions revoke --key FILE SLUG VERSION OUTPUT
                                                         create an offline revocation bundle
  abyssreader-server definitions rotate                   endorse a new key and re-sign every entry
  abyssreader-server definitions health [SLUG]            show aggregate definition error counts
  abyssreader-server admin users reset-password EMAIL  print a new temporary password
  abyssreader-server admin users disable EMAIL         switch an account off (403 account_disabled)
  abyssreader-server admin users enable EMAIL          switch a disabled account on again
  abyssreader-server admin import [--remove-chapters] FOLDER
                                                       import FOLDER/manifest.json into the catalog
                                                       (schemas/import-manifest.schema.json);
                                                       --remove-chapters lets it remove the hosted
                                                       chapters the manifest no longer lists
  abyssreader-server admin titles remove REF           remove the title with this ref
  abyssreader-server admin covers retry                download every cover URL that failed again
  abyssreader-server admin backup list                  show generations, completeness and unavailable images
  abyssreader-server admin backup now                   snapshot the database and images in /data/backups
  abyssreader-server admin backup export GENERATION     write a portable backup archive to stdout
  abyssreader-server admin errors test                 send one test event to ABYSS_ERROR_REPORTING_DSN
  abyssreader-server healthcheck                       exit 0 when the server answers /healthz
  abyssreader-server status                            exit 0 when the server answers /api/status, and
                                                       print its name and features
  abyssreader-server setup --work DIR --image REF      the questions of install.sh (docs/install.md):
                                                       reads DIR/current.env, DIR/answers.env, writes DIR/out/
  abyssreader-server version
Settings come from environment variables (ABYSS_…); see .env.example.
```

## install.sh

```text
Usage:
  sudo sh install.sh [install] [--answers FILE]   install the AbyssReader server
  sudo sh install.sh configure [--answers FILE]   change its settings, then restart it
  sudo sh install.sh upgrade [X.Y.Z | --check]
  sudo sh install.sh rollback
  sudo sh install.sh restore [--yes] [--allow-definition-rollback] BACKUP.tar
  sudo sh install.sh recover [--abandon]
  sudo sh install.sh uninstall
  sh install.sh help

Without --answers the setup asks its questions in this terminal. An answers file has the
.env format (KEY=value lines); docs/install.md lists its settings.
```
