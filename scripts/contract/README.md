<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Contract validation

CI lints `contract/openapi.yaml` with Redocly and checks the contract rules,
licence headers and examples with `validate_server_contract.py`. The validator
also has a `--self-test` that verifies it catches deliberate breakage. Install
the Node dependencies with `npm ci` from this directory and the hash-locked
Python dependencies with `pip install --require-hashes --no-deps -r requirements.txt`
using Python 3.11. Run Redocly from `contract/` so it reads the contract configuration.

`source-definition.schema.json` is a validation fixture from the app's repository.
Its schema rules are kept as a local copy so the validator can check the
`getDefinition` example offline. The fixture sits outside the published contract tree.
