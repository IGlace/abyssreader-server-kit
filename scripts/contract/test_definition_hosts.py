# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from jsonschema import Draft202012Validator

from validate_server_contract import DEFINITION_SCHEMA, ExampleChecker, load_spec


class DefinitionHostsTest(unittest.TestCase):
    def setUp(self):
        example_path = Path(__file__).resolve().parents[2] / "contract/examples/getDefinition.json"
        self.example = json.loads(example_path.read_text(encoding="utf-8"))["response"]["body"]
        self.definition = json.loads(self.example["content"])

    def test_authoring_and_contract_share_host_rules(self):
        checker = ExampleChecker(load_spec())
        schema = json.loads(DEFINITION_SCHEMA.read_text(encoding="utf-8"))
        Draft202012Validator.check_schema(schema)
        for host, valid in (
            ("example.com", True),
            ("*.cmzcdn.org", True),
            ("*.sample.github.io", True),
            ("*.www.ck", True),
            ("*", False),
            ("*.com", False),
            ("*.co.uk", False),
            ("*.github.io", False),
            ("*.foo.ck", False),
            ("*.*.example.com", False),
            ("cdn.*.example.com", False),
            ("cdn*.example.com", False),
            ("*.example.local", False),
            ("*.127.0.0.1", False),
            ("*.example.com:443", False),
            ("*.example.com\n", False),
        ):
            with self.subTest(host=host):
                self.definition["hosts"] = [host]
                content = json.dumps(self.definition)
                body = {**self.example, "content": content,
                        "contentHash": hashlib.sha256(content.encode()).hexdigest()}
                self.assertEqual(not checker.check_definition(body), valid)

    def test_authors_can_validate_a_standalone_definition(self):
        for host, valid in (("*.cmzcdn.org", True), ("*.co.uk", False)):
            with self.subTest(host=host), tempfile.TemporaryDirectory() as directory:
                self.definition["hosts"] = [host]
                path = Path(directory) / "definition.json"
                path.write_text(json.dumps(self.definition), encoding="utf-8")
                result = subprocess.run(
                    [sys.executable, str(Path(__file__).with_name("validate_server_contract.py")),
                     "--definition", str(path)],
                    capture_output=True, text=True, check=False,
                )
                self.assertEqual(result.returncode, 0 if valid else 1, result.stderr)
                if not valid:
                    self.assertIn("hosts/0", result.stderr)
