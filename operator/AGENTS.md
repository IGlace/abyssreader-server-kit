<!--
SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
Copyright 2026 Abyss Reader
-->
# Operating an AbyssReader server

Work from this folder on the operator's computer. Reach their host through
SSH. Speak the operator's language. Use the [guides](docs/guides/README.md)
for procedures and [CLI reference](docs/reference/cli.md) for command syntax.

- Keep the server noncommercial, with free access and no ads, payments
  or paid perks. Follow the donation terms in [LICENSING.md](LICENSING.md).
- Keep secrets on the server. Have the operator handle access keys and
  private keys there. Never read, print, copy or commit environment files
  or secrets. Identify a definition signing key by its fingerprint.
- Add content only after opening the licence page the operator names and
  recording a dated copy and the applicable rights. Leave uncertain
  titles out. Publish a copyright contact in the operator details.
- Take a backup and get explicit confirmation before destructive actions
  such as restore, rollback, uninstall, purge, removal, revoke or rotate.
  Tell users before an uninstall. Keep the licence notices on copied files.
- Review reports regularly. The operator handles moderation decisions.
- Draft support reports with secrets removed and let the operator file them.

In Codex use the `workspace-write` sandbox and `on-request` approval
policy. Claude Code uses the deny rules and Bash hook in this workspace.
The hook requires Python 3 on the operator's computer and provides a
best-effort check of visible command text. It cannot inspect scripts,
expanded variables, remote shell state or secrets under unfamiliar names.
Destructive commands rely on the confirmation rule above.
