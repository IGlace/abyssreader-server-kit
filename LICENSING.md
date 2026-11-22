<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Licensing

The AbyssReader server kit is **source-available**, not open source: you may read, run, change and share it for free, but never to make money.

Not legal advice. This page is the plain reading of the licences by Abyss Reader; the licence texts themselves are what bind.

## Which licence covers what

| Part | Licence | File |
|---|---|---|
| Code, scripts, the server contract (`contract/`: OpenAPI, schemas, examples), AI skills, `AGENTS.md` / `CLAUDE.md` and other agent files | PolyForm Noncommercial 1.0.0 | [`LICENSE`](LICENSE) |
| Prose documentation (this file, the READMEs, `docs/`) | Creative Commons Attribution-NonCommercial-ShareAlike 4.0 International (CC BY-NC-SA 4.0) | [`LICENSE-DOCS`](LICENSE-DOCS) |

Each file says which licence it is under in its first lines (`SPDX-License-Identifier`). The copyright holder is **Abyss Reader**. Every copy you pass on must carry the licence and the `Required Notice:` lines in [`NOTICE`](NOTICE).

## What "commercial" means here

The kit exists so that anyone can run a **community server** for free. Anything that earns money from the kit, a server built from it, or its users is commercial, and is not allowed. For example:

- paid membership, VIP or anything like it, paid features, or paid access to a server;
- ads, sponsored content or affiliate links on a server or in what it serves;
- selling the kit, a copy of it, or a changed version;
- paid hosting, paid installation or paid running of a server built from the kit;
- a company running a server built from the kit as part of its business.

This holds for private copies and changed versions too: a changed kit is still under the same licence.

## Donations

A server operator may accept **donations**, only like this:

1. **Perk-free.** Donors get nothing in return: no perks, features, early access, badges or lifted limits. A perk is paid membership in disguise, and so commercial.
2. **Cost-only.** The money only covers the server's running costs (hosting, domain, storage). This works on trust: operators do not have to publish or report their costs.
3. **Asked for only outside the app**, for example on the operator's own website or Discord. A donation request never appears in anything the app shows: the server name, the operator details, title descriptions, chapters or pages.

**Sponsorships are forbidden.** A company paying for a logo, a mention or a link is advertising.

## Permanent

The licence is permanent. The kit is never relicensed, and **no commercial licence is offered to anyone**, Abyss Reader included.

## Contributions

Pull requests are welcome; merging them is the owner's call. Contributions come in under the kit's own licences (GitHub Terms of Service D.6, "inbound = outbound"): PolyForm Noncommercial 1.0.0 for code, skills and agent files, CC BY-NC-SA 4.0 for prose docs. There is no CLA and no DCO.

## Dependencies

Libraries linked into the kit are under permissive licences (MIT, BSD, Apache-2.0 and similar), whose notices are kept. The kit links no GPL, AGPL or SSPL library. Separate programs it talks to (a database server, a reverse proxy) keep their own licences.
