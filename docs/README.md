# Proxier documentation

Proxier is a self-hosted control panel for one person's proxy setup, shared with the members they add. It builds VPS proxy servers from templates and checks whether they work from Russia. It gives people and devices subscription links, keeps domain-based routing on MikroTik routers and the Shadowrocket config in sync, and hosts the router setup script.

Every module is built and runs on the real deployment. The facts that only real hardware, accounts or phones can confirm are listed in [open questions](./open-questions.md).

## Map

| Path | What it holds |
|---|---|
| [`../CONTEXT.md`](../CONTEXT.md) | The glossary. Use its words, not the ones it lists under _Avoid_. |
| [overview.md](./overview.md) | Why Proxier exists, who uses it, the modules, what is out of scope, and the backlog. |
| [architecture.md](./architecture.md) | Runtime shape, the module system, jobs, events, data, HTTP surfaces, security, configuration, tech stack. |
| [development.md](./development.md) | Code layout, module interfaces, the rules every change follows, test harnesses, checking pages in a real browser. |
| [data-model.md](./data-model.md) | Every module's entities and how they relate; retention. |
| [events.md](./events.md) | Every event Proxier records, and which ones send a Telegram notification by default. |
| [deployment.md](./deployment.md) | How Proxier runs on blackberry behind sh-main: containers, CI, the data volume, backups. |
| [open-questions.md](./open-questions.md) | Facts not yet confirmed on the real deployment. |
| [modules/](./modules/) | One document per module: platform, servers, subscriptions, routing, router scripts. |
| [processes/](./processes/README.md) | Every flow step by step, with its rules and its edge cases (each one a test). |
| [integrations/](./integrations/) | Contracts with outside systems: VLESS/XHTTP, Cloudflare, check-host.net, Telegram, RouterOS, domain sources, Shadowrocket, the subscription format. |
| [ui/README.md](./ui/README.md) | Every screen, the navigation and the shared components: what each screen does. |
| [ui/design/](./ui/design/README.md) | How the UI looks: the rules, `tokens.css` and the source of every designed screen. |
| [adr/](./adr/) | Decisions that are hard to reverse, each with its reason. |

## Reading order

- **Code**: [architecture.md](./architecture.md), [development.md](./development.md), [adr/](./adr/), [data-model.md](./data-model.md), the module doc, then the process doc of each flow before touching it. For a screen, [ui/README.md](./ui/README.md) says what it does and [ui/design/](./ui/design/README.md) how it looks.
- **Design**: [overview.md](./overview.md), [ui/README.md](./ui/README.md), the module docs, then the process docs of the flows a screen serves.

## Conventions

- **Process docs** share one shape: **Actors**, a short introduction, **Steps** (numbered; `→ event.name` marks the event a step records), **Rules**, and **Edge cases (each is a test)**.
- **ADRs** are short: the context, the decision and its reason, and the rejected options worth remembering. A decision gets an ADR when it is hard to reverse, surprising and a real trade-off. Anything else goes into the relevant doc.
- **Defaults** (intervals, thresholds, limits) are in the module docs. Settings can change them unless the doc says otherwise.
- Times are stored in UTC and shown in the display time zone (default `Europe/Moscow`).
- The UI is in English and Russian. Docs, code and logs are in English.
- A doc describes Proxier as it is now. History lives in git, not in the docs.
