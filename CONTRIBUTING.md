<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Contributing to the AbyssReader server kit

Use the Go version pinned by the kit's CI workflow. Run these commands
from the source tree before proposing a change:

```sh
go build ./...
go vet ./...
go test -race ./...
scripts/check-licence-headers.sh
```

The Go tests include OpenAPI response checks. The [server contract](contract/README.md)
defines the app-facing API. Use `sqlc generate` and `sqlc diff` after query or
schema changes. When changing the CLI, regenerate the [CLI reference](docs/reference/cli.md)
from the built binary. Never add the private conformance suite or try to reconstruct it.

Keep the kit noncommercial and preserve the licence and required notices.
See [LICENSING.md](LICENSING.md) for contribution terms. There is no CLA.
Use permissive dependencies, excluding GPL, AGPL, and SSPL.

The [developer guides](docs/developers/README.md) explain the architecture
and how to build and run a fork image. The [customize-kit skill](.agents/skills/customize-kit/SKILL.md)
follows those guides.
