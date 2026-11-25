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

The validator checks the `getDefinition` example offline against the
[published definition schema](../../contract/definition.schema.json).

To check a resolved definition before signing it, run
`python scripts/contract/validate_server_contract.py --definition path/to/definition.json`.
The command uses the same schema as the contract example and reports invalid fields.

The `hosts` list accepts exact lowercase DNS names and single-label wildcards.
For example, `*.cdn.example.org` covers `x9.cdn.example.org`. The parent
`cdn.example.org` and deeper host `a.x9.cdn.example.org` need separate entries.
Wildcards must occupy the entire leftmost label and cannot sit directly
under a public or private suffix. The bundled suffix patterns come from
the same Public Suffix List snapshot as the app's definition schema.
