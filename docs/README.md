# Proxier documentation

Proxier is a self-hosted control panel for one person's proxy setup. It builds VPS proxy servers from templates and watches whether they work from Russia. It gives people and devices subscription links, keeps domain-based routing on MikroTik routers and the Shadowrocket config in sync, and hosts the router setup script.

**Status (2026-10-07):** the documentation and the design are done, and the UI stack is chosen (templ + htmx, [ADR 0014](./adr/0014-server-rendered-ui-templ-htmx.md)). Phase 0 ([roadmap.md](./roadmap.md)) is built in sub-phases, one per session; each has a contract (files, tables, signatures, decisions, test checklist) in [build/](./build/README.md). **0a, the skeleton** (binary, SQLite with per-module migrations, vault, settings, event log, image, CI) is done. Next is **0b, sign-in and the UI shell**.

**Design:** [ui/design/](./ui/design/README.md) holds the rules, `tokens.css` and every screen's source. The live canvas is at <https://claude.ai/artifact/TM7E3qaH69dDpV2aY4axec> (Rosé Pine page; the Console page is an earlier look kept for reference).

## How the docs are organised

| Path | What it holds |
|---|---|
| [`../CONTEXT.md`](../CONTEXT.md) | The glossary. Every term used here is defined there. Use those words, not the ones it lists under _Avoid_. |
| [overview.md](./overview.md) | Why Proxier exists, what it replaces, goals, non-goals, scope of the first version. |
| [architecture.md](./architecture.md) | Runtime shape, the module system, jobs, events, data, HTTP surfaces, security model, tech stack. |
| [data-model.md](./data-model.md) | Entities of every module and how they relate; retention. |
| [events.md](./events.md) | Every event Proxier records, and which ones send a Telegram notification by default. |
| [deployment.md](./deployment.md) | How Proxier runs on blackberry behind sh-main; configuration; backups. |
| [roadmap.md](./roadmap.md) | Build phases with exit criteria, and the "later" backlog. |
| [open-questions.md](./open-questions.md) | Decisions not taken yet and things to verify during the build. |
| [modules/](./modules/) | One document per module: platform, servers, subscriptions, routing, router scripts. |
| [processes/](./processes/README.md) | Every business flow step by step, with its rules and its edge cases written as a test list. |
| [integrations/](./integrations/) | Contracts with the outside world: VLESS/XHTTP, Cloudflare, check-host.net, Telegram, RouterOS, domain sources, Shadowrocket, subscription format. |
| [ui/README.md](./ui/README.md) | Screen inventory, navigation and shared components: what each screen does. |
| [ui/design/](./ui/design/README.md) | The final visual design: rules, `tokens.css`, and the source of every screen. |
| [build/](./build/README.md) | Build contracts, one per sub-phase (Phases 0–3), and what each one built. |
| [adr/](./adr/) | Decisions that are hard to reverse, each with the reason it was taken. |

## Reading order

- **Design** (Claude Design): [overview.md](./overview.md) → [ui/README.md](./ui/README.md) → the module docs → the process docs for the key flows that `ui/README.md` lists.
- **Build**: [architecture.md](./architecture.md) → [adr/](./adr/) → [data-model.md](./data-model.md) → the module doc → each process doc before touching its flow. For any screen, read [ui/README.md](./ui/README.md) for what it does and [ui/design/](./ui/design/README.md) for how it looks.

## Conventions

- **Process docs** follow one shape: **Actors**, a short introduction, **Steps** (numbered; `→ event.name` marks the event a step records), **Rules**, and **Edge cases (each is a test)**. An edge case listed there must have a test once the flow is built.
- **ADRs** are short: the context, the decision and the reason, plus rejected options when they're worth remembering. A new decision that is hard to reverse, surprising and a real trade-off gets an ADR. Anything else is just written into the relevant doc.
- **Defaults** (intervals, thresholds, limits) are given in the module docs and can be changed in Settings unless the doc says otherwise.
- Times are shown in the configured time zone (default `Europe/Moscow`). Stored times are UTC.
- Proxier has English and Russian UI from the first version. Docs, code and logs are English.
