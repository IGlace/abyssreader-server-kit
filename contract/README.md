<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Server contract

The **server contract** describes the endpoints every AbyssReader server must offer (core features) and those it may offer (optional features). Server implementers and the app's conformance tests use the same contract.

The server kit's `contract/` folder is the source of truth. The official server's repository keeps a copy pinned to a kit tag for its tests and the app's tests.

| Path | What |
|---|---|
| `openapi.yaml` | OpenAPI 3.1: the fixed `GET /api/status` and every contract v1 route under `/api/v1`. |
| `examples/<operationId>.json` | One example per operation (request and response). |
| `examples/<operationId>.<case>.json` | Extra cases: error bodies, `304`, `409`, `429`. |
| [`definition.schema.json`](definition.schema.json) | Schema for resolved source definitions. See the [family/source merge rules](definition-merge.md). |
| `sync/` | The sync part, referenced unchanged: `sync.schema.json` plus its fixtures and worked examples (see `sync/README.md`). |
| `redocly.yaml`, `.redocly.lint-ignore.yaml` | Lint configuration (Redocly `recommended-strict`) and its two accepted exceptions. |

## Reading the OpenAPI file

- **`x-feature`** on each operation: `core`, or the optional feature's name exactly as `/api/status` lists it (`comments`, `collections`, `stats`, `reports`, `backup`, `google-sign-in`, `hosted-chapters`).
- **`x-auth`**: `none`, `optional` (a signed-in caller gets personal fields such as `me`) or `required`.
- **`x-error-codes`** on each non-2xx response: the contract's own `code` values for it. `code` is an open enum: a server may add codes of its own with the same status (see Checks).
- **`x-enum: open | closed`** on every enum. Closed enums list their values in `enum`; open ones list today's values in `x-values`.
- **`x-may-be-absent`**: the one response field a server may leave out (`Error.fields`). Every other response field is in `required` and always sent, `null` when it has no value.
- The general rules (addresses, versions, access key, session, errors, lists, ids, timestamps, images, limits) are in `info.description` at the top of the file.

## Example format

```json
{
  "licence": "SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0",
  "notice": "Required Notice: …",
  "operationId": "getTitle",
  "summary": "…",
  "request": { "method": "GET", "path": "/api/v1/titles/42", "query": {}, "headers": {}, "body": {} },
  "response": { "status": 200, "headers": {}, "body": {} }
}
```

`query` values are typed JSON (a repeated parameter is an array). A non-JSON response (the backup file) has `"body": null` and a `bodyNote`. A `400` example may send a request that breaks the contract on purpose. Example data is invented: no real sites, people or servers. The Ed25519 keys and signatures in the definition examples are real and verify.

## Checks

The kit's Go tests check server responses against `openapi.yaml`, including response bodies, HTTP statuses and error codes. Run `go test ./...` from the kit root. The [kit CI workflow](https://github.com/IGlace/abyssreader-server-kit/blob/main/.github/workflows/ci.yml) runs these tests with the race detector.

The official server's repository checks its pinned copy against the kit tag. The kit lints the OpenAPI document with Redocly and validates every example against its operation.

**Error codes are open.** `x-error-codes` lists the contract's own codes for a response. A server may send other snake_case codes with the same status and the same meaning of that status; the app handles them by their status. The validator accepts such codes in examples, but rejects a contract code used under a status the file does not list it for.

## Changing the contract

- Tag route and field changes as `contract-vX.Y`, where `X.Y` is the contract version in `info.version`. Increment the major version for a breaking change or the minor version for an addition, as defined in `info.description`.
- For a notice or README-only revision, use a separate docs label such as `contract-v1.0-docs1`. The tag `contract-v1.0` still identifies the routes and fields, and `info.version` stays `1.0`.
- Keep one example per operation and update its request and response when the operation changes.
- Never edit `sync/sync.schema.json` for the server contract: the sync part changes only with the sync contract.

## Licence

The machine-readable files (OpenAPI, schemas, examples and lint configuration) use [PolyForm Noncommercial 1.0.0](https://github.com/IGlace/abyssreader-server-kit/blob/main/LICENSE). This README and other prose use [CC BY-NC-SA 4.0](https://github.com/IGlace/abyssreader-server-kit/blob/main/LICENSE-DOCS). Copyright Abyss Reader.
