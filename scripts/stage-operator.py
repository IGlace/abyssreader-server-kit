#!/usr/bin/env python3
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Build a deterministic operator workspace from the scanned candidate source."""

import argparse
from pathlib import Path, PurePosixPath
import re
import stat
import tarfile
import zipfile


def collect(source, installer):
    files = {"install.sh": (installer.read_bytes(), 0o755)}
    bundled_dirs = {"contract", "docs", "examples", "schemas"}
    bundled_files = {"CONTRIBUTING.md", "LICENSE", "LICENSE-DOCS", "LICENSING.md", "NOTICE", "SECURITY.md"}
    with tarfile.open(source) as archive:
        for entry in archive:
            path = PurePosixPath(entry.name)
            if path.is_absolute() or ".." in path.parts or not path.parts:
                raise ValueError("Invalid path in candidate archive.")
            if entry.isdir():
                continue
            head = path.parts[0]
            if head == "operator":
                name = str(PurePosixPath(*path.parts[1:]))
                if name.startswith(".claude/skills/"):
                    raise ValueError("Claude skills must be generated from the agent skills.")
            elif (head in bundled_dirs or str(path) in bundled_files or
                  path.parts[:3] == (".agents", "skills", "customize-kit")):
                name = str(path)
            else:
                continue
            if not entry.isfile() or name in files:
                raise ValueError(f"Expected a unique regular workspace file: {name}")
            mode = 0o755 if entry.mode & 0o111 else 0o644
            files[name] = (archive.extractfile(entry).read(), mode)
    required = {
        "README.md", "AGENTS.md", "CLAUDE.md", ".gitignore",
        ".claude/settings.json", "docs/guides/README.md", "docs/reference/cli.md",
        "docs/developers/README.md", "CONTRIBUTING.md", "LICENSING.md", "LICENSE", "LICENSE-DOCS", "NOTICE",
    }
    if missing := required - files.keys():
        raise ValueError("Workspace is missing " + ", ".join(sorted(missing)))
    skills = {p: item for p, item in files.items() if p.startswith(".agents/skills/")}
    if not skills:
        raise ValueError("Workspace has no agent skills.")
    for path, item in skills.items():
        files[path.replace(".agents/", ".claude/", 1)] = item
    return files


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("version")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?", args.version):
        parser.error("pass an exact release version")
    root = f"abyssreader-operator-{args.version}"
    files = collect(args.source, args.output / "install.sh")
    with zipfile.ZipFile(args.output / f"{root}.zip", "w") as workspace:
        for path, (body, mode) in sorted(files.items()):
            entry = zipfile.ZipInfo(f"{root}/{path}")
            entry.create_system = 3
            entry.external_attr = (stat.S_IFREG | mode) << 16
            workspace.writestr(entry, body)


if __name__ == "__main__":
    main()
