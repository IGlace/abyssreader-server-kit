# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Independently compare a partial preservation archive to the live data before damage."""
import hashlib
import json
import sqlite3
import sys
import tarfile
import tempfile
from pathlib import Path


def images(archive):
    return {member.name: archive.extractfile(member).read() for member in archive
            if member.name.startswith(("covers/", "pages/")) and member.isfile()}


def dump(database):
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory, "db")
        path.write_bytes(database)
        with sqlite3.connect(path) as db:
            assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
            return list(db.iterdump())


with tarfile.open(sys.argv[2]) as source:
    original = images(source)
    if sys.argv[1] == "choose":
        for category in ("covers/", "pages/"):
            print(next(name for name in sorted(original) if name.startswith(category)))
    else:
        with tarfile.open(sys.argv[3]) as saved:
            manifest = json.load(saved.extractfile("backup.json"))
            database = saved.extractfile("abyssreader.db").read()
            missing = set(sys.argv[4:])
            assert manifest["incomplete"] is True
            assert set(manifest["missing"]) == missing and len(manifest["missing"]) == len(missing)
            assert hashlib.sha256(database).hexdigest() == manifest["databaseSha256"]
            assert dump(database) == dump(source.extractfile("abyssreader.db").read())
            assert images(saved) == {name: data for name, data in original.items() if name not in missing}
