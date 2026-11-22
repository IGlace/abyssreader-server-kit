# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Leave an actual restore prompt unanswered during an ordinary service restart."""
import subprocess
import sys
import time
from pathlib import Path

script, install, archive, log = sys.argv[1:]
with Path(log).open("wb") as report:
    restore = subprocess.Popen(["sh", script, "restore", archive], stdin=subprocess.PIPE,
                               stdout=report, stderr=subprocess.STDOUT)
    try:
        deadline = time.monotonic() + 60
        while b"type restore" not in Path(log).read_bytes().lower():
            if restore.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("installer did not reach restore confirmation")
            time.sleep(0.1)
        subprocess.run(["docker", "compose", "restart", "server"], cwd=install, check=True, timeout=30)
        deadline = time.monotonic() + 12
        while True:
            status = subprocess.run(["docker", "compose", "exec", "-T", "server", "abyssreader-server", "status"],
                                    cwd=install, capture_output=True)
            if status.returncode == 0:
                break
            if time.monotonic() > deadline:
                raise RuntimeError("ordinary boot was blocked while waiting for RESTORE")
            time.sleep(0.2)
        if restore.poll() is not None:
            raise RuntimeError("restore proceeded without confirmation")
    finally:
        if restore.poll() is None:
            restore.communicate(b"cancel\n", timeout=90)
    assert restore.returncode != 0, "cancellation unexpectedly succeeded"
