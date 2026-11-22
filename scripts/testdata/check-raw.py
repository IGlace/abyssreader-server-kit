# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Check exact bytes of the stopped database and sidecars without opening SQLite."""
import hashlib
import json
import sys
import tarfile
from pathlib import Path

with tarfile.open(sys.argv[1]) as archive:
    meta = json.load(archive.extractfile("backup.json"))
    assert meta["raw"] is True and meta["rawSha256"]
    entries = dict(line.split("  ", 1)[::-1] for line in Path(sys.argv[2]).read_text().splitlines())
    assert set(entries) == {"abyssreader.db" + s for s in ("", "-wal", "-shm", "-journal")}
    for filename, sha in entries.items():
        assert hashlib.sha256(archive.extractfile(filename).read()).hexdigest() == sha, filename

if len(sys.argv) == 4:
    with tarfile.open(sys.argv[3]) as whole:
        members = {m.name.removeprefix("./"): m for m in whole if m.isfile()}
        assert any(n.startswith("backups/kit-") for n in members), "not a full volume archive"
        for filename, sha in entries.items():
            assert hashlib.sha256(whole.extractfile(members[filename]).read()).hexdigest() == sha
