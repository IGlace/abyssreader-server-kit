#!/usr/bin/env python3
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Generate the CLI reference and check commands and links in the operator help."""

import argparse
from collections import defaultdict
from itertools import product
from pathlib import Path
import re
import shlex
import subprocess
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parent.parent
TOOLS = r"abyssreader-server|install\.sh"
COMMAND = re.compile(r"(?<![\w./-])(?:\./)?(" + TOOLS + r")(?=[ \t]|$)([^\n`#]*)")
FLAG = re.compile(r"--[a-z][a-z0-9-]*")
LINK = re.compile(r"\]\(<?([^>\s)]+)>?(?:\s+\"[^\"]*\")?\)")
CONTRACT_PATH = re.compile(r"`(contract/[^\s`]+)`")


def parse_help(tool, help_text):
    commands = defaultdict(set)
    for line in help_text.splitlines():
        usage = re.match(r"^\s+(?:sudo sh |sh )?" + re.escape(tool) + r" (.+)", line)
        if usage is None:
            continue
        syntax = re.split(r"\s{2,}", usage[1].strip(), maxsplit=1)[0]
        path = []
        for term in syntax.split():
            word = term.strip("[]")
            options = word.split("|")
            if not all(re.fullmatch(r"[a-z][a-z0-9-]*", option) for option in options):
                break
            if term.startswith("["):
                options.append("")
            path.append(options)
        for words in product(*path):
            commands[tuple(word for word in words if word)].update(FLAG.findall(syntax))
    return commands


def check_command(tool, arguments, commands):
    try:
        words = shlex.split(re.split(r"\s+&&|\s+\|\s+|\s+>|\s*;\s*|\)\s*(?:&&|$)", arguments)[0])
    except ValueError:
        return None  # A shell example can continue its quotation on the next line.
    if not words or words[0] in ("--help", "-h", "help"):
        return None
    if words[0] == "--" or words[0].startswith((".", "to ")):
        return None
    matching = [p for p in commands if tuple(words[:len(p)]) == p]
    path = max(matching, key=len, default=None)
    if path is None or (not path and not words[0].startswith("-")):
        return f"unknown command {tool} {' '.join(words)}"
    remainder = words[len(path):]
    if path and remainder and not remainder[0].startswith("-"):
        # Reject typos where this command has documented subcommands but no
        # standalone syntax other than flags or a subcommand.
        if any(p[:len(path)] == path and len(p) > len(path) for p in commands):
            if tool == "abyssreader-server" and path[:1] != ("admin",):
                return f"unknown subcommand {tool} {' '.join(words)}"
    for word in remainder:
        if word == "--":
            break
        if word.startswith("--"):
            flag = word.split("=", 1)[0]
            if flag not in commands[path] and flag not in ("--help",):
                return f"unknown flag {flag} for {tool} {' '.join(path)}"
    return None


def link_target(root, file, url):
    parsed = urlsplit(url)
    if parsed.scheme or parsed.netloc:
        return None, ""
    path = unquote(parsed.path)
    target = (file.parent / path).resolve() if path else file
    if target.is_relative_to(root / "operator") and not target.exists():
        # The packaged workspace has the same docs, schemas and examples at its root.
        suffix = target.relative_to(root / "operator")
        if suffix.parts and suffix.parts[0] in {
            "contract", "docs", "examples", "schemas", "LICENSE", "LICENSE-DOCS",
            "CONTRIBUTING.md", "LICENSING.md", "NOTICE", "SECURITY.md", "install.sh",
        }:
            target = root / suffix
    return target, unquote(parsed.fragment)


def markdown_anchors(file):
    counts = defaultdict(int)
    for heading in re.findall(r"^#{1,6}\s+(.+)$", file.read_text(), re.MULTILINE):
        slug = re.sub(r"[^\w -]", "", heading.lower()).replace(" ", "-")
        yield f"{slug}-{counts[slug]}" if counts[slug] else slug
        counts[slug] += 1


def check_file(root, file, help_texts):
    checks = {tool: parse_help(tool, menu) for tool, menu in help_texts.items()}
    errors = []
    content = file.read_text()
    for link in LINK.findall(content):
        target, fragment = link_target(root, file, link)
        if target is None:
            continue
        if not target.is_relative_to(root) or not target.exists():
            errors.append(f"broken link {link}")
        elif fragment and target.suffix == ".md" and fragment not in markdown_anchors(target):
            errors.append(f"broken heading link {link}")
    for path in CONTRACT_PATH.findall(content):
        if not (root / path).exists():
            errors.append(f"missing contract path {path}")
    fence = False
    for line in content.splitlines():
        if line.lstrip().startswith("```"):
            fence = not fence
            continue
        # Check shell commands and inline code. Prose sometimes names the tool
        # without giving a command, or quotes the installer's filename.
        if fence and not line.lstrip().startswith("#"):
            snippets = [line]
        else:
            snippets = re.findall(r"(?<!`)`([^`]+)`(?!`)", line)
        for snippet in snippets:
            for cmd in COMMAND.finditer(snippet):
                error = check_command(cmd[1], cmd[2], checks[cmd[1]])
                if error:
                    errors.append(error)
    return errors


def render_reference(help_texts):
    return (
        "<!--\nSPDX-License-Identifier: CC-BY-NC-SA-4.0\nCopyright 2026 Abyss Reader\n-->\n"
        "# CLI reference\n\nGenerated from each command's help with `scripts/operator-docs.py`.\n"
        + "".join(f"\n## {tool}\n\n```text\n{text.rstrip()}\n```\n" for tool, text in help_texts.items())
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("generate", "check"))
    parser.add_argument("binary", type=Path, help="built abyssreader-server")
    args = parser.parse_args()
    help_texts = {
        "abyssreader-server": subprocess.check_output([args.binary.resolve(), "--help"], text=True),
        "install.sh": subprocess.check_output(["sh", ROOT / "install.sh", "--help"], text=True),
    }
    reference = ROOT / "docs/reference/cli.md"
    generated = render_reference(help_texts)
    if args.action == "generate":
        reference.write_text(generated)
        return
    errors = []
    if not reference.exists() or reference.read_text() != generated:
        errors.append("docs/reference/cli.md differs from --help. Generate it again.")
    paths = {
        ROOT / "CONTRIBUTING.md", ROOT / "CLAUDE.md",
        ROOT / "docs/install.md", ROOT / "docs/reports-and-purge.md",
    }
    for directory in ("docs/guides", "docs/reference", "docs/developers", "operator", ".agents/skills"):
        paths.update((ROOT / directory).rglob("*.md"))
    for file in sorted(paths - {reference}):
        errors.extend(f"{file.relative_to(ROOT)}: {msg}" for msg in check_file(ROOT, file, help_texts))
    if errors:
        raise SystemExit("\n".join(errors))


if __name__ == "__main__":
    main()
