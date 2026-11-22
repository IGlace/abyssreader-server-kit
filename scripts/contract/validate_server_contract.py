# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
# Required Notice: Copyright 2026 Abyss Reader (https://abyssreader.com)
# Required Notice: The server kit is licensed for noncommercial use only, under the PolyForm Noncommercial License 1.0.0 (LICENSE). It is never relicensed, and no commercial licence is offered to anyone. See LICENSING.md.
"""Checks the server contract: the OpenAPI file's own rules, then every example against it.

Run from the repo root:

    python scripts/contract/validate_server_contract.py            # check the contract and examples
    python scripts/contract/validate_server_contract.py --self-test  # prove the checks catch breakage

Structural OpenAPI 3.1 validity comes from `openapi-spec-validator`; style lint from Redocly CLI
(see .github/workflows/ci.yml). This script adds the contract's own rules:

* every operation names its feature (`x-feature`), its auth (`x-auth`), and the error codes of every
  non-2xx response (`x-error-codes`); optional-feature routes answer `404 feature_disabled`; every
  v1 route documents `401 session_invalid`, the access-key 403s, 429 and a default error;
* every response field is required (servers always send every field they declare), except fields
  listed in `x-may-be-absent`;
* every enum is marked `x-enum: open` (values in `x-values`) or `x-enum: closed` (values in `enum`);
* nothing about the official server's paid membership or its extra routes appears anywhere;
* the licence header is on every file;
* every operation that takes path or query parameters or a body documents a `400`, and no GET or
  DELETE takes a body;
* there is exactly one example per operation (`examples/<operationId>.json`; extra cases are
  `examples/<operationId>.<case>.json`), and each example's request and response validate against
  the operation. Examples are checked strictly: an object field the schema does not declare fails.
  Paged examples must be consistent (`items` within `size`, `hasMore` and `totalItems` agree), and
  a definition example's `content` must validate against the definition schema.

`x-error-codes` lists the contract's own codes per response. A server may send other snake_case
codes with the same status, so an example may carry a code outside the list, but never a contract
code listed only under another status.
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any, Iterator
from urllib.parse import quote

import yaml
from jsonschema import Draft202012Validator
from openapi_spec_validator import validate as validate_openapi
from referencing import Registry, Resource
from referencing.exceptions import Unresolvable
from referencing.jsonschema import DRAFT202012

ROOT = Path(__file__).resolve().parents[2]
CONTRACT_DIR = ROOT / "contract"
OPENAPI = CONTRACT_DIR / "openapi.yaml"
EXAMPLES = CONTRACT_DIR / "examples"
SYNC_SCHEMA = CONTRACT_DIR / "sync" / "sync.schema.json"
# The definition schema is pinned as a validation fixture beside this script. See README.md.
DEFINITION_SCHEMA = Path(__file__).with_name("source-definition.schema.json")
HEADER_FILES = ("openapi.yaml", "redocly.yaml", ".redocly.lint-ignore.yaml")

LICENCE_ID = "SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0"
NOTICE = "Required Notice: Copyright 2026 Abyss Reader"
METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
FIXED_PATHS = {"/api/status"}
V1_PREFIX = "/api/v1/"
# Operations that cannot carry a session or an access key (a public status page, a browser tab).
NO_HEADER_OPS = {"getStatus", "startGoogleSignIn"}
FORBIDDEN = re.compile(r"vip|paidservices|discordid|/api/official", re.IGNORECASE)
ACCESS_KEY_CODES = {"access_key_required", "access_key_invalid"}


class ContractError(Exception):
    pass


# ───────────────────────────── loading ─────────────────────────────

def load_spec(path: Path = OPENAPI) -> dict:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def operations(spec: dict) -> Iterator[tuple[str, str, dict]]:
    for path, item in spec["paths"].items():
        for method, op in item.items():
            if method in METHODS:
                yield path, method, op


def resolve_local(spec: dict, node: Any) -> tuple[Any, str | None]:
    """Follows one-hop local `$ref`s of OpenAPI objects; returns (object, json-pointer or None)."""
    pointer = None
    while isinstance(node, dict) and "$ref" in node and node["$ref"].startswith("#/"):
        pointer = node["$ref"][1:]
        try:
            node = pointer_get(spec, pointer)
        except (KeyError, IndexError, ValueError, TypeError):
            raise ContractError(f"dangling $ref {node['$ref']!r}") from None
    return node, pointer


def pointer_get(doc: Any, pointer: str) -> Any:
    for part in pointer.lstrip("/").split("/"):
        part = part.replace("~1", "/").replace("~0", "~")
        doc = doc[int(part)] if isinstance(doc, list) else doc[part]
    return doc


def pointer_join(*parts: str) -> str:
    return "/" + "/".join(p.replace("~", "~0").replace("/", "~1") for p in parts)


# ───────────────────────────── contract rules ─────────────────────────────

def check_header(path: Path, errors: list[str]) -> None:
    head = path.read_text(encoding="utf-8").splitlines()[:6]
    if not any(LICENCE_ID in line for line in head) or not any(NOTICE in line for line in head):
        errors.append(f"{path.relative_to(ROOT)}: missing the PolyForm NC licence header / Required Notice")


def walk_schemas(node: Any, where: str) -> Iterator[tuple[dict, str]]:
    """Every dict that looks like a schema, anywhere in the document (good enough for marker checks)."""
    if isinstance(node, dict):
        yield node, where
        for k, v in node.items():
            yield from walk_schemas(v, f"{where}/{k}")
    elif isinstance(node, list):
        for i, v in enumerate(node):
            yield from walk_schemas(v, f"{where}/{i}")


def feature_names(spec: dict) -> set[str]:
    return set(spec["components"]["schemas"]["FeatureName"]["x-values"])


def response_schema_refs(spec: dict) -> set[str]:
    """Names of component schemas reachable from any response body."""
    seen: set[str] = set()
    todo: list[Any] = []
    for _, _, op in operations(spec):
        for resp in op.get("responses", {}).values():
            resp, _ = resolve_local(spec, resp)
            for media in (resp.get("content") or {}).values():
                todo.append(media.get("schema"))
    while todo:
        node = todo.pop()
        if isinstance(node, dict):
            ref = node.get("$ref", "")
            if ref.startswith("#/components/schemas/"):
                name = ref.rsplit("/", 1)[1]
                if name not in seen:
                    seen.add(name)
                    todo.append(spec["components"]["schemas"][name])
            todo.extend(node.values())
        elif isinstance(node, list):
            todo.extend(node)
    return seen


def check_all_required(schema: Any, where: str, errors: list[str]) -> None:
    if isinstance(schema, dict):
        props = schema.get("properties")
        if isinstance(props, dict) and schema.get("type") == "object":
            absent_ok = set(schema.get("x-may-be-absent", []))
            missing = set(props) - set(schema.get("required", [])) - absent_ok
            if missing:
                errors.append(f"{where}: response fields not in `required`: {sorted(missing)}")
            for name, sub in props.items():
                check_all_required(sub, f"{where}.{name}", errors)
        for key in ("items", "anyOf", "oneOf", "allOf"):
            sub = schema.get(key)
            if isinstance(sub, list):
                for i, s in enumerate(sub):
                    check_all_required(s, f"{where}.{key}[{i}]", errors)
            elif isinstance(sub, dict):
                check_all_required(sub, f"{where}.{key}", errors)


def check_contract(spec: dict, raw_text: str) -> list[str]:
    try:
        return _check_contract(spec, raw_text)
    except ContractError as e:
        return [f"openapi.yaml: {e}"]


def _check_contract(spec: dict, raw_text: str) -> list[str]:
    errors: list[str] = []
    features = feature_names(spec) | {"core"}

    hit = FORBIDDEN.search(raw_text)
    if hit:
        errors.append(f"openapi.yaml: forbidden official-only term {hit.group(0)!r}")

    op_ids: set[str] = set()
    for path, method, op in operations(spec):
        oid = op.get("operationId")
        label = f"{method.upper()} {path}"
        if not oid:
            errors.append(f"{label}: no operationId")
            continue
        if oid in op_ids:
            errors.append(f"{label}: duplicate operationId {oid}")
        op_ids.add(oid)
        if path not in FIXED_PATHS and not path.startswith(V1_PREFIX):
            errors.append(f"{oid}: path {path} is neither /api/status nor under /api/v1/")
        feature = op.get("x-feature")
        if feature not in features:
            errors.append(f"{oid}: x-feature {feature!r} is not core or an optional feature name")
        if op.get("x-auth") not in ("none", "optional", "required"):
            errors.append(f"{oid}: x-auth must be none, optional or required")
        if not op.get("tags"):
            errors.append(f"{oid}: no tag")

        responses = op.get("responses", {})
        if "default" not in responses:
            errors.append(f"{oid}: no default (5xx) response")
        if "429" not in responses:
            errors.append(f"{oid}: no 429 response")
        codes_by_status: dict[str, set[str]] = {}
        for status, resp in responses.items():
            resp_obj, _ = resolve_local(spec, resp)
            if status.startswith(("2", "3")):
                continue
            if "x-error-codes" not in resp_obj:
                errors.append(f"{oid} {status}: no x-error-codes")
                continue
            codes_by_status[status] = set(resp_obj["x-error-codes"])
            schema = ((resp_obj.get("content") or {}).get("application/json") or {}).get("schema", {})
            is_sync_conflict = oid == "syncPull" and status == "409"
            if not is_sync_conflict and schema.get("$ref") != "#/components/schemas/Error":
                errors.append(f"{oid} {status}: body is not the Error schema")
        if feature != "core" and "feature_disabled" not in codes_by_status.get("404", set()):
            errors.append(f"{oid}: optional feature route without 404 feature_disabled")
        if oid not in NO_HEADER_OPS:
            if codes_by_status.get("401") != {"session_invalid"}:
                errors.append(f"{oid}: 401 must be exactly session_invalid")
            if not ACCESS_KEY_CODES <= codes_by_status.get("403", set()):
                errors.append(f"{oid}: 403 must include access_key_required and access_key_invalid")
        if op.get("requestBody") and method in ("get", "delete"):
            # DELETE bodies are dropped by some proxies and clients (owner decision 2026-11-19).
            errors.append(f"{oid}: {method.upper()} with a request body")
        takes_input = bool(op.get("requestBody")) or any(
            resolve_local(spec, p)[0].get("in") in ("path", "query")
            for p in spec["paths"][path].get("parameters", []) + op.get("parameters", []))
        if takes_input and "400" not in responses:
            errors.append(f"{oid}: takes parameters or a body but documents no 400")

    for schema, where in walk_schemas(spec, "#"):
        marker = schema.get("x-enum")
        if "enum" in schema and marker != "closed":
            errors.append(f"{where}: enum without `x-enum: closed`")
        if "x-values" in schema and marker != "open":
            errors.append(f"{where}: x-values without `x-enum: open`")
        if marker is not None and marker not in ("open", "closed"):
            errors.append(f"{where}: x-enum must be open or closed")
        if marker == "open" and "x-values" not in schema:
            errors.append(f"{where}: open enum without x-values")
        if marker == "closed" and "enum" not in schema:
            errors.append(f"{where}: closed enum without enum")

    for name in sorted(response_schema_refs(spec)):
        check_all_required(spec["components"]["schemas"][name], f"components.schemas.{name}", errors)
    return errors


# ───────────────────────────── examples ─────────────────────────────

def strict_copy(node: Any) -> Any:
    """The document with `unevaluatedProperties: false` on every object schema that declares properties."""
    node = copy.deepcopy(node)
    for schema, _ in walk_schemas(node, "#"):
        if (isinstance(schema.get("properties"), dict) and schema.get("type") == "object"
                and "additionalProperties" not in schema and "unevaluatedProperties" not in schema):
            schema["unevaluatedProperties"] = False
    return node


class ExampleChecker:
    def __init__(self, spec: dict):
        self.spec = spec
        self.base = OPENAPI.resolve().as_uri()
        sync_doc = json.loads(SYNC_SCHEMA.read_text(encoding="utf-8"))
        sync_res = Resource.from_contents(sync_doc)
        openapi_res = DRAFT202012.create_resource(strict_copy(spec))
        self.registry = Registry().with_resources([
            (self.base, openapi_res),
            (SYNC_SCHEMA.resolve().as_uri(), sync_res),
            (sync_doc["$id"], sync_res),
        ])
        self.contract_codes = set(spec["components"]["schemas"]["Error"]["properties"]["code"]["x-values"])
        self.definition_validator = Draft202012Validator(
            json.loads(DEFINITION_SCHEMA.read_text(encoding="utf-8")),
            format_checker=Draft202012Validator.FORMAT_CHECKER)
        self.ops: dict[str, tuple[str, str, dict]] = {
            op["operationId"]: (path, method, op) for path, method, op in operations(spec)
        }

    def validate(self, instance: Any, pointer: str) -> list[str]:
        validator = Draft202012Validator(
            {"$ref": f"{self.base}#{quote(pointer, safe='/~')}"},
            registry=self.registry,
            format_checker=Draft202012Validator.FORMAT_CHECKER,
        )
        try:
            found = sorted(validator.iter_errors(instance), key=lambda e: list(e.absolute_path))
        except Unresolvable as e:
            return [f"dangling $ref: {e}"]
        return [f"{'/'.join(map(str, e.absolute_path)) or '(root)'}: {e.message}" for e in found]

    def parameters(self, path: str, method: str, op: dict) -> list[tuple[dict, str]]:
        """(parameter, json-pointer of its schema) for path-level and operation-level parameters."""
        found = []
        sources = [(self.spec["paths"][path].get("parameters", []), ("paths", path, "parameters")),
                   (op.get("parameters", []), ("paths", path, method, "parameters"))]
        for params, at in sources:
            for i, p in enumerate(params):
                obj, ptr = resolve_local(self.spec, p)
                found.append((obj, (ptr or pointer_join(*at, str(i))) + "/schema"))
        return found

    def check_paging(self, req: dict, body: Any, params: list[tuple[dict, str]]) -> list[str]:
        """A paged body agrees with itself and with the request's `page` and `size`."""
        if not (isinstance(body, dict) and {"items", "page", "size", "totalItems", "hasMore"} <= set(body)):
            return []
        errs = []
        query = req.get("query", {})
        size_param = next((p for p, _ in params if p["in"] == "query" and p["name"] == "size"), None)
        size_default = ((resolve_local(self.spec, size_param.get("schema", {}))[0] if size_param else {})
                        .get("default"))
        want_size = query.get("size", size_default)
        want_page = query.get("page", 1)
        items, page, size, total, more = (body[k] for k in ("items", "page", "size", "totalItems", "hasMore"))
        if page != want_page:
            errs.append(f"paging: page {page} but the request asked for page {want_page}")
        if want_size is not None and size != want_size:
            errs.append(f"paging: size {size} but the request asked for {want_size}")
        if len(items) > size:
            errs.append(f"paging: {len(items)} items on a page of size {size}")
        seen = (page - 1) * size + len(items)
        if more and (len(items) != size or seen >= total):
            errs.append(f"paging: hasMore is true, but this page ends the list ({seen} of {total})")
        if not more and seen != total:
            errs.append(f"paging: hasMore is false, but items up to this page ({seen}) are not totalItems ({total})")
        return errs

    def check_definition(self, body: dict) -> list[str]:
        try:
            definition = json.loads(body.get("content", ""))
        except ValueError as e:
            return [f"definition content is not JSON: {e}"]
        errs = [f"definition content: {'/'.join(map(str, e.absolute_path)) or '(root)'}: {e.message}"
                for e in self.definition_validator.iter_errors(definition)]
        if hashlib.sha256(body["content"].encode("utf-8")).hexdigest() != body.get("contentHash"):
            errs.append("definition contentHash is not the SHA-256 of content")
        return errs

    def check(self, file: Path, example: dict) -> list[str]:
        errs: list[str] = []
        for key in ("licence", "notice", "operationId", "summary", "request", "response"):
            if key not in example:
                errs.append(f"missing top-level `{key}`")
        if errs:
            return errs
        if example["licence"] != LICENCE_ID or not example["notice"].startswith(NOTICE):
            errs.append("licence/notice do not carry the PolyForm NC header")
        oid = example["operationId"]
        if oid not in self.ops:
            return errs + [f"unknown operationId {oid}"]
        stem = file.name.split(".")[0]
        if stem != oid:
            errs.append(f"file name {file.name} does not start with its operationId {oid}")
        path_tpl, method, op = self.ops[oid]
        req, resp = example["request"], example["response"]
        extra = set(req) - {"method", "path", "query", "headers", "body"}
        extra |= {f"response.{k}" for k in set(resp) - {"status", "headers", "body", "bodyNote"}}
        if extra:
            errs.append(f"unknown keys {sorted(extra)}")

        # A 400 example shows a request that breaks the contract on purpose: its parameters and
        # body are not schema-checked (the path template, headers and response still are).
        req_errs: list[str] = []
        status = int(resp["status"])

        # method and path
        if req.get("method", "").lower() != method:
            errs.append(f"request.method {req.get('method')} is not {method.upper()}")
        names = re.findall(r"\{([^}]+)\}", path_tpl)
        rx = "^" + re.sub(r"\\\{[^}]+\\\}", "([^/]+)", re.escape(path_tpl)) + "$"
        m = re.match(rx, req.get("path", ""))
        params = self.parameters(path_tpl, method, op)
        if not m:
            errs.append(f"request.path {req.get('path')} does not match {path_tpl}")
        else:
            for name, raw in zip(names, m.groups()):
                ptr = next((ptr for p, ptr in params if p["in"] == "path" and p["name"] == name), None)
                if ptr is None:
                    errs.append(f"path parameter {name} is not declared")
                    continue
                value: Any = int(raw) if raw.isdigit() else raw
                req_errs += [f"path {name}: {e}" for e in self.validate(value, ptr)]

        # query and headers
        query = req.get("query", {})
        declared_q = {p["name"]: (p, ptr) for p, ptr in params if p["in"] == "query"}
        for name, value in query.items():
            if name not in declared_q:
                errs.append(f"query parameter {name} is not declared")
            else:
                req_errs += [f"query {name}: {e}" for e in self.validate(value, declared_q[name][1])]
        for name, (p, _) in declared_q.items():
            if p.get("required") and name not in query:
                errs.append(f"required query parameter {name} missing")
        headers = {k.lower(): v for k, v in req.get("headers", {}).items()}
        for p, ptr in params:
            if p["in"] == "header":
                v = headers.get(p["name"].lower())
                if v is None and p.get("required"):
                    errs.append(f"required header {p['name']} missing")
                elif v is not None:
                    req_errs += [f"header {p['name']}: {e}" for e in self.validate(v, ptr)]
        if not re.match(r"^AbyssReader/[0-9]+\.[0-9]+\.[0-9]+ \((android|ios)\)$", headers.get("user-agent", "")):
            if oid != "startGoogleSignIn":
                errs.append("request.headers User-Agent must be `AbyssReader/<version> (<android|ios>)`")
        if op.get("x-auth") == "required" and 200 <= status < 300 and "authorization" not in headers:
            errs.append("a signed-in route's success example must send Authorization")
        if oid in NO_HEADER_OPS and ({"authorization", "x-access-key"} & set(headers)):
            errs.append("this route takes no Authorization or X-Access-Key")

        # request body
        body_spec = op.get("requestBody")
        if body_spec:
            body_spec, body_ptr = resolve_local(self.spec, body_spec)
            if body_ptr is None:
                body_ptr = pointer_join("paths", path_tpl, method, "requestBody")
            if "body" not in req:
                if body_spec.get("required"):
                    errs.append("request.body missing")
            else:
                media = next(iter(body_spec["content"]))
                ptr = f"{body_ptr}/content/{media.replace('/', '~1')}/schema"
                req_errs += [f"request.body: {e}" for e in self.validate(req["body"], ptr)]
        elif "body" in req:
            errs.append("request.body given, but the operation takes none")

        if status != 400:
            errs += req_errs

        # response
        responses = op["responses"]
        key = str(status) if str(status) in responses else ("default" if status >= 500 else None)
        if key is None:
            return errs + [f"response.status {status} is not declared"]
        r_obj, r_ptr = resolve_local(self.spec, responses[key])
        if r_ptr is None:
            r_ptr = pointer_join("paths", path_tpl, method, "responses", key)
        r_headers = {k.lower(): v for k, v in resp.get("headers", {}).items()}
        for h_name, h in (r_obj.get("headers") or {}).items():
            h_obj, h_ptr = resolve_local(self.spec, h)
            if h_ptr is None:
                h_ptr = f"{r_ptr}/headers/{h_name}"
            v = r_headers.get(h_name.lower())
            if v is None:
                if h_obj.get("required"):
                    errs.append(f"response header {h_name} missing")
            else:
                errs += [f"response header {h_name}: {e}" for e in self.validate(v, f"{h_ptr}/schema")]
        content = r_obj.get("content") or {}
        if "application/json" in content:
            if "body" not in resp:
                errs.append("response.body missing")
            else:
                errs += [f"response.body: {e}" for e in
                         self.validate(resp["body"], f"{r_ptr}/content/application~1json/schema")]
                codes = r_obj.get("x-error-codes")
                code = resp["body"].get("code") if isinstance(resp["body"], dict) else None
                if codes and code in self.contract_codes and code not in codes:
                    errs.append(f"response.body.code {code!r} is a contract code, but not one of "
                                f"this response's x-error-codes {codes}")
                errs += self.check_paging(req, resp["body"], params)
                if oid == "getDefinition" and status == 200 and isinstance(resp["body"], dict):
                    errs += self.check_definition(resp["body"])
        elif content:
            if resp.get("body") is not None:
                errs.append("a non-JSON response must have `body: null` (describe it in `bodyNote`)")
        elif resp.get("body") is not None:
            errs.append(f"response {status} has no body")
        return errs


def example_files() -> list[Path]:
    return sorted(EXAMPLES.glob("*.json"))


def check_examples(spec: dict, files: list[Path] | None = None,
                   loaded: dict[Path, dict] | None = None) -> tuple[list[str], int]:
    checker = ExampleChecker(spec)
    files = files if files is not None else example_files()
    errors: list[str] = []
    main_by_op: dict[str, list[Path]] = {}
    for f in files:
        example = loaded[f] if loaded and f in loaded else json.loads(f.read_text(encoding="utf-8"))
        text = json.dumps(example)
        hit = FORBIDDEN.search(text)
        if hit:
            errors.append(f"examples/{f.name}: forbidden official-only term {hit.group(0)!r}")
        for e in checker.check(f, example):
            errors.append(f"examples/{f.name}: {e}")
        if f.name.count(".") == 1:
            main_by_op.setdefault(f.stem, []).append(f)
    for oid in sorted(checker.ops):
        if oid not in main_by_op:
            errors.append(f"examples/{oid}.json: missing (one example per operation)")
    for oid in sorted(set(main_by_op) - set(checker.ops)):
        errors.append(f"examples/{oid}.json: no operation with this operationId")
    return errors, len(files)


# ───────────────────────────── entry points ─────────────────────────────

def run() -> int:
    errors: list[str] = []
    for f in [CONTRACT_DIR / name for name in HEADER_FILES] + [Path(__file__)]:
        check_header(f, errors)
    raw = OPENAPI.read_text(encoding="utf-8")
    spec = load_spec()
    try:
        validate_openapi(spec, base_uri=OPENAPI.resolve().as_uri())
    except Exception as e:  # the validator's own exception types vary by version
        first = str(e).strip().splitlines()[0] if str(e).strip() else type(e).__name__
        errors.append(f"openapi.yaml is not valid OpenAPI 3.1: {first[:300]}")
    errors += check_contract(spec, raw)
    try:
        ex_errors, count = check_examples(spec)
    except ContractError as e:
        ex_errors, count = [f"openapi.yaml: {e}"], 0
    errors += ex_errors
    n_ops = sum(1 for _ in operations(spec))
    if errors:
        print("\n".join(errors))
        print(f"FAILED: {len(errors)} problem(s)")
        return 1
    print(f"OK: openapi.yaml is valid OpenAPI {spec['openapi']}, contract {spec['info']['version']}, "
          f"{n_ops} operations; {count} example files validate (one per operation + error cases).")
    return 0


def self_test() -> int:
    """Breaks copies of the contract and examples in memory; every break must be caught."""
    spec = load_spec()
    raw = OPENAPI.read_text(encoding="utf-8")
    files = example_files()
    loaded = {f: json.loads(f.read_text(encoding="utf-8")) for f in files}
    baseline, _ = check_examples(spec, files, loaded)
    if baseline or check_contract(spec, raw):
        print("self-test: the unmodified contract must pass first")
        return 1

    def ex(name: str) -> Path:
        return next(f for f in files if f.name == name)

    cases: list[tuple[str, Any]] = []

    def broken_example(label: str, name: str, mutate) -> None:
        mod = copy.deepcopy(loaded)
        mutate(mod[ex(name)])
        cases.append((label, lambda m=mod: check_examples(spec, files, m)[0]))

    def broken_spec(label: str, mutate) -> None:
        s = copy.deepcopy(spec)
        mutate(s)
        cases.append((label, lambda s=s: check_contract(s, yaml.safe_dump(s))))

    broken_example("missing required response field", "getTitle.json",
                   lambda e: e["response"]["body"].pop("rating"))
    broken_example("undeclared response field", "getTitle.json",
                   lambda e: e["response"]["body"].__setitem__("chapterViews", 3))
    broken_example("null in a non-null field", "getStatus.json",
                   lambda e: e["response"]["body"]["operator"].__setitem__("name", None))
    broken_example("closed enum value", "getLibrary.json",
                   lambda e: e["request"]["query"].__setitem__("list", "library"))
    broken_example("undeclared status", "getMe.json",
                   lambda e: e["response"].__setitem__("status", 202))
    broken_example("error code not allowed for the status", "signIn.wrong-password.json",
                   lambda e: e["response"]["body"].__setitem__("code", "session_invalid"))
    broken_example("timestamp without millis", "getTitle.json",
                   lambda e: e["response"]["body"].__setitem__("createdAt", "2026-03-02T09:15:00Z"))
    broken_example("sync item breaking sync.schema.json", "syncPush.json",
                   lambda e: e["request"]["body"]["items"][0].__setitem__("kind", "library"))
    broken_example("official-only term in an example", "getMe.json",
                   lambda e: e["response"]["body"].__setitem__("vipUntil", None))
    broken_example("page size above 100", "searchTitles.json",
                   lambda e: e["request"]["query"].__setitem__("size", 500))
    broken_example("missing ETag header", "getDefinitionManifest.json",
                   lambda e: e["response"]["headers"].clear())
    broken_example("title report without a reason", "reportTitle.json",
                   lambda e: e["request"]["body"].pop("reason"))
    broken_example("title report with today's details-only body", "reportTitle.json",
                   lambda e: e["request"].__setitem__("body", {"details": "a" * 501}))
    broken_example("a title with the old `cover` name", "getTitle.json",
                   lambda e: e["response"]["body"].__setitem__("cover", e["response"]["body"].pop("coverUrl")))
    broken_example("account deletion without the password field", "deleteAccount.json",
                   lambda e: e["request"].__setitem__("body", {}))
    broken_spec("DELETE with a request body",
                lambda s: s["paths"]["/api/v1/comments/{id}"]["delete"].__setitem__(
                    "requestBody", s["paths"]["/api/v1/me/deletion"]["post"]["requestBody"]))
    broken_spec("optional route without feature_disabled",
                lambda s: s["paths"]["/api/v1/rankings"]["get"]["responses"].__setitem__(
                    "404", {"$ref": "#/components/responses/NotFound"}))
    broken_spec("response field not required",
                lambda s: s["components"]["schemas"]["Title"]["required"].remove("me"))
    broken_spec("enum without open/closed marker",
                lambda s: s["components"]["schemas"]["TitleSort"].pop("x-enum"))
    broken_spec("route without a feature", lambda s: s["paths"]["/api/v1/me"]["get"].pop("x-feature"))
    broken_example("paging: totalItems disagrees with hasMore", "listChapters.json",
                   lambda e: e["response"]["body"].__setitem__("hasMore", False))
    broken_example("definition content breaks the definition schema", "getDefinition.json",
                   lambda e: e["response"]["body"].__setitem__(
                       "content", e["response"]["body"]["content"].replace('"schemaVersion":1', '"schemaVersion":2')))
    broken_spec("route with parameters but no 400",
                lambda s: s["paths"]["/api/v1/titles/{id}"]["get"]["responses"].pop("400"))
    broken_spec("dangling $ref is a clean error",
                lambda s: s["paths"]["/api/v1/me"]["get"]["responses"].__setitem__(
                    "401", {"$ref": "#/components/responses/NoSuchResponse"}))
    cases.append(("missing example for an operation",
                  lambda: check_examples(spec, [f for f in files if f.name != "listGenres.json"], loaded)[0]))

    failed = 0
    for label, run_case in cases:
        caught = run_case()
        print(f"{'ok  ' if caught else 'MISS'} {label}" + (f"  ({caught[0]})" if caught else ""))
        failed += 0 if caught else 1
    print(f"self-test: {len(cases) - failed}/{len(cases)} breaks caught")
    return 1 if failed else 0


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--self-test", action="store_true", help="prove that the checks catch breakage")
    args = parser.parse_args()
    sys.exit(self_test() if args.self_test else run())
