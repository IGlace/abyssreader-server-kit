#!/usr/bin/env python3
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader

import os
from pathlib import Path
import pty
import subprocess
import tempfile
import unittest

INSTALLER = Path(__file__).resolve().parent.parent / "install.sh"
IMAGE = "local-build:review"


class CustomImageInstallerTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.work = Path(temp.name)
        self.env = {
            **os.environ,
            "PATH": f"{self.work}{os.pathsep}{os.environ['PATH']}",
            "ABYSS_INSTALL_DIR": str(self.work / "not-installed"),
            "ABYSS_INSTALL_TEST": "",
            "KIT_FIXTURE_UID": "0",
        }
        for name, command in {
            "id": 'printf "%s\\n" "$KIT_FIXTURE_UID"',
            "uname": "printf 'Linux\\n'",
            "docker": "touch docker-called; exit 1",
        }.items():
            script = self.work / name
            script.write_text("#!/bin/sh\n" + command + "\n")
            script.chmod(0o755)

    def run_installer(self, answers, terminal=False):
        cmd = ["sh", str(INSTALLER), "--answers", answers]
        if not terminal:
            result = subprocess.run(cmd, cwd=self.work, env=self.env, input="",
                                    capture_output=True, text=True, timeout=5)
            return result.returncode, result.stdout + result.stderr
        master, slave = pty.openpty()
        try:
            with subprocess.Popen(cmd, cwd=self.work, env=self.env, stdin=slave,
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                  text=True) as proc:
                os.write(master, b"n\n")
                try:
                    output, _ = proc.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.communicate()
                    raise
                return proc.returncode, output
        finally:
            os.close(slave)
            os.close(master)

    def test_non_root_error_precedes_reading_image_setting(self):
        (self.work / "answers.env").write_text("KIT_IMAGE=invalid\n")
        self.env["KIT_FIXTURE_UID"] = "1001"
        code, output = self.run_installer("answers.env")
        self.assertNotEqual(code, 0)
        self.assertIn("run it as root", output)
        self.assertNotIn("invalid KIT_IMAGE", output)
        self.assertFalse((self.work / "docker-called").exists())

    def test_dash_prefixed_answers_path_selects_and_announces_image(self):
        (self.work / "-answers.env").write_text(f"KIT_IMAGE={IMAGE}\n")
        code, output = self.run_installer("-answers.env")
        self.assertNotEqual(code, 0)  # The Docker stub stops work after selection.
        self.assertIn(IMAGE, output)
        self.assertIn("Support covers official images only", output)
        self.assertTrue((self.work / "docker-called").exists(), output)

    def test_interactive_decline_never_calls_docker(self):
        (self.work / "answers.env").write_text(f"KIT_IMAGE={IMAGE}\n")
        code, output = self.run_installer("answers.env", terminal=True)
        self.assertNotEqual(code, 0)
        self.assertIn(IMAGE, output)
        self.assertIn("[y/N]", output)
        self.assertIn("Custom image declined", output)
        self.assertFalse((self.work / "docker-called").exists())


if __name__ == "__main__":
    unittest.main()
