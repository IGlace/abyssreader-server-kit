<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# App support schedule

A kit release has its own version. The app connects using the server contract version listed by the server.

The kit embeds the app support schedule as of the release build. If a served contract is less than 90 days from losing app support, the kit warns in its startup log, once a day while it runs, and in the setup health report. A planned minimum minor above the one the kit serves also triggers the warning. Check the release notes for served versions and scheduled dates.

Run `sudo sh install.sh upgrade --check` to compare the installed and latest official kit releases and check the live app support schedule. This action reads the public schedule at `https://abyssreader.com/app/compat.json` without sending server data. The running server uses its embedded copy. If the installed image predates this check, open the public schedule and compare it with the contract versions the server reports.

A kit minor can add features or support a new contract minor. A kit major is required when an operator must act, for example when a kit drops a contract major or requires a manual migration. Each kit release serves the latest minor for each major it offers. Fixes, including security fixes, go into the newest kit minor.
