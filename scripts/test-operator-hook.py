#!/usr/bin/env python3
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

WORKSPACE = Path(__file__).resolve().parent.parent / "operator"


class OperatorHookTests(unittest.TestCase):
    def setUp(self):
        self.settings = json.loads((WORKSPACE / ".claude/settings.json").read_text())
        self.hook = self.settings["hooks"]["PreToolUse"][0]["hooks"][0]["command"]

    def run_hook(self, data, env=None):
        env = {**(os.environ if env is None else env), "CLAUDE_PROJECT_DIR": str(WORKSPACE)}
        return subprocess.run(
            ["/bin/sh", "-c", self.hook], cwd=WORKSPACE, input=data,
            text=True, capture_output=True, env=env,
        )

    def bash_input(self, command):
        return json.dumps({
            "hook_event_name": "PreToolUse", "tool_name": "Bash",
            "tool_input": {"command": command},
        })

    def assert_command(self, command, denied):
        result = self.run_hook(self.bash_input(command))
        self.assertEqual(0, result.returncode, result.stderr)
        response = json.loads(result.stdout) if result.stdout else {}
        self.assertEqual(denied, response.get("hookSpecificOutput", {}).get("permissionDecision") == "deny")
        self.assertNotIn(command, result.stdout + result.stderr)

    def test_settings_deny_documented_secret_paths(self):
        self.assertNotIn("$comment", self.settings)
        for tool in ("Read", "Edit"):
            for path in ("**/*.env", "**/*.key", "**/*.pem", "**/private-offline.key"):
                with self.subTest(tool=tool, path=path):
                    self.assertIn(f"{tool}({path})", self.settings["permissions"]["deny"])

    def test_secret_paths_in_remote_commands(self):
        for path in (
            "/tmp/abyss-answers.env",
            "/data/keys/signing.key",
            "/data/offline.pem",
            "private-offline.key",
            "/data/secrets/config.json",
        ):
            with self.subTest(path=path):
                self.assert_command(f"ssh host 'cat -- {path}'", denied=True)

    def test_shell_patterns_that_can_read_secret_paths(self):
        for command in (
            "ssh host 'cat /opt/abyssreader-server/.e*'",
            "ssh host 'cat /opt/abyssreader-server/.en?'",
            "ssh host 'cat /opt/abyssreader-server/.e[n]v'",
            "ssh host 'cat /opt/abyssreader-server/.[e]nv'",
            "ssh host 'cat /opt/abyssreader-server/.*'",
            r"cat $'.e\156v'",
        ):
            with self.subTest(command=command):
                self.assert_command(command, denied=True)

    def test_docker_commands_that_print_credentials(self):
        for command in (
            "ssh host 'sudo docker compose config'",
            "ssh host 'sudo docker compose --project-directory /opt/abyssreader-server config'",
            "ssh host 'sudo docker inspect server'",
            "docker container inspect server",
            "docker compose exec -T server env",
            "ssh host 'sudo docker compose exec -T server printenv ABYSS_ACCESS_KEY'",
        ):
            with self.subTest(command=command):
                self.assert_command(command, denied=True)

    def test_harmless_prose_and_non_secret_paths_pass(self):
        for command in (
            'git commit -m "keep secrets out"',
            "docker compose logs server | grep -i secrets",
            "docker compose exec -T server echo env",
            "ssh host 'docker compose ps'",
            'cat .editorconfig',
            'git status --porcelain',
        ):
            with self.subTest(command=command):
                self.assert_command(command, denied=False)

    def test_malformed_input_exits_2_without_echo(self):
        for data in ("{", "null", "{}", self.bash_input({"bad": "payload"})):
            with self.subTest(data=data):
                result = self.run_hook(data)
                self.assertEqual(2, result.returncode)
                self.assertEqual("", result.stdout)
                self.assertNotIn("Traceback", result.stderr)
                self.assertNotIn("payload", result.stderr)

    def test_missing_interpreter_exits_2(self):
        with tempfile.TemporaryDirectory() as empty_path:
            result = self.run_hook(self.bash_input("git status"), env={"PATH": empty_path})
        self.assertEqual(2, result.returncode)
        self.assertEqual("", result.stdout)


if __name__ == "__main__":
    unittest.main()
