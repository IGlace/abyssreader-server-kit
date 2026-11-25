#!/usr/bin/env python3
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class OperatorDocsTests(unittest.TestCase):
    def test_bad_command_flag_and_link_fail(self):
        spec = importlib.util.spec_from_file_location("operator_docs", ROOT / "scripts" / "operator-docs.py")
        docs = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(docs)
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            (root / "exists.md").touch()
            guide = root / "guide.md"
            menus = {
                "abyssreader-server": "  abyssreader-server doctor [--expected-ip IP]\n"
                                      "  abyssreader-server admin import [--remove-chapters] DIR\n",
                "install.sh": "  sudo sh install.sh [install] [--answers FILE]\n"
                              "  sudo sh install.sh upgrade [X.Y.Z | --check]\n",
            }
            guide.write_text("Run `abyssreader-server doctor --expected-ip 192.0.2.1` or "
                             "`sh install.sh --answers .env`. [Guide](exists.md)\n")
            self.assertEqual([], docs.check_file(root, guide, menus))
            guide.write_text("Run `abyssreader-server doctro` and "
                             "`sh install.sh upgrade --answers .env`. [Guide](absent.md)\n")
            errors = docs.check_file(root, guide, menus)
            for text in ("doctro", "--answers", "absent.md"):
                self.assertTrue(any(text in error for error in errors), errors)

    def test_settings_and_hook_deny_secret_reads(self):
        settings = json.loads((ROOT / "operator/.claude/settings.json").read_text())
        denied = settings["permissions"]["deny"]
        for rule in ("Read(**/.env)", "Read(**/secrets/**)", "Read(**/definition-signing-key)"):
            self.assertIn(rule, denied)
        hook = settings["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
        for command, expected in (
            ("ssh operator@example.org 'cat /opt/abyssreader-server/.env'", True),
            ("ssh operator@example.org 'head /data/secrets/definition-signing-key'", True),
            ("ssh operator@example.org 'cat /opt/app/.env.local'", True),
            ("scp operator@example.org:/opt/app/.env ./copy", True),
            ("ssh operator@example.org 'docker compose ps'", False),
            ("ssh operator@example.org 'ls -al'", False),
        ):
            with self.subTest(command=command):
                result = subprocess.run(
                    hook, shell=True, cwd=ROOT / "operator", check=True, text=True, capture_output=True,
                    input=json.dumps({"hook_event_name": "PreToolUse", "tool_name": "Bash",
                                      "tool_input": {"command": command}}),
                )
                response = json.loads(result.stdout) if result.stdout else {}
                self.assertEqual(expected, response.get("hookSpecificOutput", {}).get("permissionDecision") == "deny")
                self.assertNotIn(command, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
