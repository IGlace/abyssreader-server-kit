# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Freeze at a real installer boundary, then close its terminal or interrupt its process group."""

import errno
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import sys
import time


def phase():
    state = Path(os.environ["TEST_INSTALL_DIR"], ".installer")
    try:
        point = (state / "pending").read_text().strip()
        return (state / point / "phase").read_text().strip()
    except FileNotFoundError:
        return None


def pause_at(event):
    if event != os.environ.get("TEST_BOUNDARY"):
        return
    reached = Path(os.environ["TEST_GATE"])
    if reached.exists():
        return  # Recovery in the same process must pass this boundary.
    reached.touch()
    deadline = time.monotonic() + 20
    while not reached.with_suffix(".resume").exists():
        if time.monotonic() > deadline:
            raise RuntimeError(f"test controller did not release {event}")
        time.sleep(0.03)


def wrap(command, args):
    real = os.environ["TEST_REAL_" + command.upper()]
    if command == "mv" and os.environ.get("TEST_BOUNDARY", "").startswith("phase/"):
        subprocess.run([real, *args], check=True)
        current = phase()
        if current:
            pause_at("phase/" + current)
        return
    if command == "sleep" and args == ["2"] and os.environ.get("TEST_FAST_RETRIES"):
        time.sleep(0.1)
        return
    if command == "docker":
        if args[:1] == ["pull"]:
            if args[1] in (os.environ["TEST_GOOD_IMAGE"], os.environ["TEST_BROKEN_IMAGE"]):
                os.execv(real, [real, "image", "inspect", args[1]])
            if args[1] == os.environ["TEST_BASE_IMAGE"]:
                # Model an evicted exact tag. Recreate it from the locally retained digest.
                os.execv(real, [real, "tag", os.environ["TEST_BASE_DIGEST"], args[1]])
            raise RuntimeError(f"unexpected pull of {args[1]} in isolated installer test")
        if args[-2:] == ["maintenance", "apply"] and os.environ.get("TEST_BOUNDARY") == "mid-swap":
            # A mounted covers/ cannot be renamed (EBUSY). The REAL helper has
            # already moved and synced the DB at this point. Freeze before the
            # installer can see its failure and undo the partial swap.
            result = subprocess.run([real, *args[:-3], "--tmpfs", "/data/covers:rw",
                                     *args[-3:]], stderr=subprocess.PIPE)
            if result.returncode == 0 or b"busy" not in result.stderr.lower():
                raise RuntimeError(f"expected EBUSY halfway through the swap: {result.stderr!r}")
            pause_at("mid-swap")
            sys.exit(result.returncode)
        if args == ["compose", "exec", "-T", "server", "abyssreader-server", "status"]:
            if os.environ.get("TEST_BOUNDARY") == "candidate-probed" and phase() in ("upgrade-start", "swap"):
                result = subprocess.run([real, *args])
                if result.returncode == 0:
                    pause_at("candidate-probed")
                sys.exit(result.returncode)
        if os.environ.get("TEST_FAIL_RECOVERY") and phase() == "recovery-start" and "up" in args:
            sys.exit(73)
    os.execv(real, [real, *args])


def interrupt(boundary, how, command):
    directory = Path(os.environ["TEST_WORK"])
    gate = directory / "gate"
    resume = gate.with_suffix(".resume")
    gate.unlink(missing_ok=True)
    resume.unlink(missing_ok=True)
    os.environ.update(TEST_GATE=str(gate), TEST_BOUNDARY=boundary)
    # A child launched as an asynchronous shell job can inherit an ignored SIGINT.
    signal.signal(signal.SIGINT, signal.SIG_DFL)
    master = None
    report = directory / f"{how}-{boundary.replace('/', '-')}.log"
    with report.open("wb") as log:
        if how == "HUP":
            pid, master = pty.fork()
            if pid == 0:
                os.execvp(command[0], command)
        else:
            with open(os.environ.get("TEST_INPUT", os.devnull), "rb") as source:
                child = subprocess.Popen(command, start_new_session=True, stdin=source,
                                         stdout=log, stderr=log)
            pid = child.pid
        deadline = time.monotonic() + 150
        sent = False
        reaped = False
        try:
            while time.monotonic() < deadline:
                if master is not None and select.select([master], [], [], 0)[0]:
                    try:
                        log.write(os.read(master, 65536))
                    except OSError as exc:
                        if exc.errno != errno.EIO:
                            raise
                if gate.exists() and not sent:
                    if how == "HUP":
                        os.close(master)  # Kernel hangup: all three terminal descriptors lose their peer.
                        master = None
                    elif how == "KILL":
                        os.killpg(pid, signal.SIGKILL)
                    else:
                        os.kill(pid, getattr(signal, "SIG" + how))
                    resume.touch()
                    sent = True
                ended, status = os.waitpid(pid, os.WNOHANG)
                if ended:
                    reaped = True
                    code = os.waitstatus_to_exitcode(status)
                    expected = {"HUP": 129, "INT": 130, "TERM": 143, "KILL": -9}[how]
                    if not sent or code != expected:
                        raise RuntimeError(f"{boundary}: sent={sent}, exit={code}, expected {expected}")
                    return
                time.sleep(0.03)
            raise RuntimeError(f"timed out waiting for {how} at {boundary}")
        finally:
            if master is not None:
                os.close(master)
            if not reaped:
                # Only kill a group whose leader is still our child. Once waitpid
                # reaps it, Darwin can return EPERM for the departed group.
                try:
                    os.killpg(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                os.waitpid(pid, 0)


if __name__ == "__main__":
    if sys.argv[1] == "interrupt":
        try:
            interrupt(sys.argv[2], sys.argv[3], sys.argv[4:])
        except RuntimeError:
            report = Path(os.environ["TEST_WORK"]) / f"{sys.argv[3]}-{sys.argv[2].replace('/', '-')}.log"
            sys.stderr.write(report.read_text(errors="replace"))
            raise
    else:
        wrap(sys.argv[1], sys.argv[2:])
