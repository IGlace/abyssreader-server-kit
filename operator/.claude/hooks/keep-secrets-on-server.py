#!/usr/bin/env python3
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
"""Best-effort Claude Code PreToolUse guard for shell credential reads."""

import json
import re
import sys


SECRET_PATH = re.compile(
    r"(?:\.env(?:\.[\w.-]+)?|\.(?:key|pem))(?![\w.-])"
    r"|(?<![\w.-])(?:secrets/|definition-signing-key(?![\w.-]))",
    re.IGNORECASE,
)
SECRET_GLOB = re.compile(r"\.(?:e[\w.-]*)?[*?\[{]", re.IGNORECASE)
DOCKER_COMMAND = re.compile(r"(?<![\w.-])(docker(?:-compose)?)\s+([^\n;&|()<>]+)")
DOCKER_OPTIONS = {"--config", "--context", "--host", "--log-level", "-c", "-H", "-l",
                  "--tlscacert", "--tlscert", "--tlskey"}
COMPOSE_OPTIONS = {"--ansi", "--env-file", "--file", "--parallel", "--profile",
                   "--progress", "--project-directory", "--project-name", "-f", "-p"}
EXEC_OPTIONS = {"--env", "--index", "--user", "--workdir", "-e", "-u", "-w"}


def without_options(words, with_value):
    args = iter(words)
    for word in args:
        if word == "--":
            return list(args)
        if not word.startswith("-"):
            return [word, *args]
        if word in with_value:
            next(args, None)
    return []


def prints_container_secrets(command):
    for docker in DOCKER_COMMAND.finditer(command):
        words = docker[2].split()
        if docker[1] == "docker":
            args = without_options(words, DOCKER_OPTIONS)
            if args[:1] == ["inspect"] or args[:2] in (["container", "inspect"], ["image", "inspect"]):
                return True
            if args[:1] != ["compose"]:
                continue
            words = args[1:]
        args = without_options(words, COMPOSE_OPTIONS)
        if args[:1] == ["config"]:
            return True
        if args[:1] == ["exec"]:
            program = without_options(args[1:], EXEC_OPTIONS)[1:2]
            if program and program[0].rsplit("/", 1)[-1] in {"env", "printenv"}:
                return True
    return False


def main():
    payload = json.load(sys.stdin)
    if payload["hook_event_name"] != "PreToolUse" or payload["tool_name"] != "Bash":
        raise ValueError("unexpected hook input")
    command = payload["tool_input"]["command"]
    if not isinstance(command, str):
        raise ValueError("invalid command")
    # Remove quoting and continuations so the check can also see command text
    # inside an SSH argument. The hook only examines input.
    normalized = re.sub(r"""\\\n|['"\\]""", "", command)
    ansi_dotenv = "$'" in command and ".e" in normalized.lower()
    if (SECRET_PATH.search(normalized) or SECRET_GLOB.search(normalized)
            or ansi_dotenv or prints_container_secrets(normalized)):
        print(json.dumps({"hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": "Secret files stay on the server. Ask the operator to handle them locally.",
        }}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("Could not check the Bash command for secret reads.", file=sys.stderr)
        sys.exit(2)
