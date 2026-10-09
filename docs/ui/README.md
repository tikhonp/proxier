# UI: screens, navigation and components

The finished visual design (tokens, rules, screen sources) is in [design/](design/README.md).

This document lists every screen, what it shows, what can be done there, and the states it must handle. It was the input for the design phase. The UI is built with templ + htmx ([ADR 0014](../adr/0014-server-rendered-ui-templ-htmx.md)). Terms are the glossary's ([CONTEXT.md](../../CONTEXT.md)). Behaviour behind each action is in the linked process docs.

## Character

- **A personal power tool.** One expert user who knows the domain. Dense, but calm, and never cluttered. The design can show IPs, versions and counts without explaining them, but must explain *verdicts* ("why is this blocked?").
- **Status first.** Most visits answer "is everything OK?" in two seconds, then go one level deeper. Health, sync and job states are the most important visual vocabulary.
- **Long work is visible.** Anything that takes more than a second is a job with a live log and step progress, shown where it was started. Nothing spins without saying what it is doing.
- **Safe by default.** Destructive or wide-reaching actions say exactly what will happen (how many links, which servers) and use a preview, a plan or a diff before applying. Irreversible ones need the name typed.
- **Desktop first, phone capable.** Day-to-day work happens on a laptop. The phone is for reacting to a Telegram notification: open the linked page, understand it, pause checks, disable a link, run a sync, copy a link URL or show its QR.
- **Two languages.** English and Russian from day one. Russian strings run about 30 % longer, so no fixed-width labels or buttons.
- **Dark only**, in Rosé Pine colours (the main variant) on a black ground, with one monospaced typeface, IBM Plex Mono. Colour means state, and every state is also a shape and a word. The full vocabulary is on the [design canvas](https://claude.ai/artifact/TM7E3qaH69dDpV2aY4axec) (Rosé Pine page).
- **Flush blocks, boxed controls.** Big blocks (header, sidebar, areas, tables, banners) run edge to edge and meet along 1 px lines. Buttons and fields keep a border and some room around them, so they always read as controls. There are two exceptions, both for backing out. **Cancel** and **Close** have no border, and a pop-up's **×** fills its top-right corner, flush against the edges like a header cell.
- **Keyboard first on desktop.** A small vim-like keymap: `j`/`k` to move, `/` to search things and actions, `:` for actions only, `g` then a letter to go to a page, `?` for the full list. A line at the bottom of every desktop screen shows the keys for what is under the cursor. Letters never fire inside a field, and nothing that deletes has a single key.

## Global layout

- **Sidebar** (collapsible on desktop, a drawer on phone), grouped:
  - **Overview**: Dashboard
  - **Servers**: Servers, Templates, Locations
  - **Subscriptions**: Subscriptions, Links
  - **Routing**: Lists, Services, Search, Discover, Routers, Shadowrocket
  - **Router scripts**
  - **System**: Jobs, Activity, Settings
- **Header**:
  - **search** (`/`): a pop-up near the top of the screen that finds servers, links, subscriptions, services, routers and router scripts by name, and the actions you can run on them, in one grouped list; `:` opens it with actions only. It has no ×: `esc` or a click outside closes it. Every button is also an action there, with the same words;
  - **jobs indicator**: the number of running jobs, opening a list with their current step, and a mark when a job failed since the last look;
  - **notifications health**: a warning if Telegram isn't configured or deliveries are failing;
  - the **admin menu** (language, sign out).
- **Key line** at the bottom of every desktop screen: the keys for the row under the cursor on the left, `/ : g ?` on the right. Hidden on a phone.
- **Breadcrumbs** on detail pages. A **page header** with the object's name, its main status badge, and its primary actions. Secondary actions sit in a "more" menu.

## Status vocabulary

Every status is a badge with an **icon + text**, never colour alone. Suggested semantics (final colours are the design's choice):

| Thing | States |
|---|---|
| Server health | healthy (green) · degraded (amber) · blocked (violet — must look different from down) · down (red) · unknown (grey) · paused (blue-grey) |
| Server lifecycle | provisioning (in progress) · active · failed (red) · retired (muted) |
| Link | active · disabled · expired · deleted (muted), plus a separate alert marker for shared-link alerts |
| Router | active · awaiting setup · paused, plus the last sync result: synced, syncing, failed, drift |
| Snapshot / service | ok · rejected snapshot waiting · refresh failing |
| Job | queued · running (with the step) · succeeded · failed · cancelled · interrupted |
| Home connectivity | online · foreign unreachable · offline (dashboard banner) |

Time is shown relative ("4 min ago") with the absolute time on hover or tap.

## Shared components

- **Status badge** (above), with an optional "since" and a reason tooltip.
- **Live log viewer**: monospaced, step markers, follow mode, copy and download, redacted secrets shown as `•••`.
- **Step progress**: a vertical list of a job's steps with state and duration. Used on job pages and embedded on server, router and discovery pages.
- **Plan / diff viewer**: file diffs (unified, with a side-by-side option), domain-list diffs (added in green, removed in red, with counts), and per-tag sync plans.
- **Code editor**: YAML, JSON, nginx, shell, RouterOS script, Shadowrocket config; a file tree for templates; inline validation markers.
- **Secret field**: masked value with **Reveal**, **Copy** and optionally **Regenerate**. Used for connection URIs, link URLs, tokens and settings.
- **QR code** panel, large enough to scan from a laptop screen.
- **Confirmation dialog** in three strengths:
  - simple;
  - with consequences listed;
  - type the name to confirm.

  Each has a title row with a flush **×**, the action button and a borderless **Cancel**; `esc` closes it.
- **Checklist table**: selectable rows with grouping, used in discovery results and imports.
- **Sortable list**: drag to reorder, used for subscription servers and routing list services.
- **Charts**: time series with ranges 24 h / 7 d / 30 d, sparklines, and small usage bars.
- **Check matrix**: rows are checks and vantage points, columns are latest result, time and a 24 h sparkline.
- **Filter bar** and **pagination** for long lists.
- **Empty states** that say what the thing is and offer the next action.
- **Messages** for quick confirmations ("Link URL copied"): on desktop they replace the keys in the key line for a few seconds; on a phone they show as a band under the header.
- **Keys list** (`?`): every key in one sheet, grouped by where it works.

## Screens

### Sign-in
Username, password, error area (generic message; lockout countdown), language follows the browser. Nothing else. → [sign-in](../processes/platform/sign-in.md)

### Dashboard
- Banners: home offline or foreign unreachable; notifications not configured or failing; first-run checklist until done (Cloudflare, Telegram, SSH keys, first server, first subscription and link, first router).
- **Fleet**: counts by health state, then every server that isn't healthy, at the top with its reason. Servers with "update available".
- **Jobs**: running now, plus failed in the last 24 h.
- **Routing** (as built in 3f, after Links, link "Routers →"): "2 routers in sync"; routers with a failing sync ("sync failed at connect · 3 min ago"), drift ("drift: youtube") or unmanaged tags ("1 unmanaged tag: old-work") first, awaiting ones with them; when none, each router with its last sync; **Snapshots** ("None waiting for a decision." or "netflix · 61 % drop · Review"); **Daily refresh** from the last digest ("04:00 · 3 services changed (+42 / −5 domains) · 0 rejected", or "not run yet"). The dashboard has no global headline, so the routers-in-sync count heads the area.
- **Links**: expiring within 7 days; shared-link alerts.
- **Recent activity**: the last 20 events.

### Servers
- **Server list**: see [servers module](../modules/servers.md#server-list). Row click opens the server. Bulk actions. Filter chips. A prominent **New server** button.
- **New server**: one form. IP, root password, location (suggested), template + version, dynamic parameter fields, notes. A **live summary** shows the name, hostnames, DNS record and endpoint names. After **Create**, it goes straight to the server page with provisioning progress. → [provisioning](../processes/servers/server-provisioning.md)
- **Server page**, with the header (flag, name, health badge with reason, lifecycle state, primary actions **Run checks now** and **Redeploy**, and a "more" menu) and tabs:
  - **Overview**: identity, template version, endpoints with connection URIs (secret field + QR), subscriptions containing it, notes, recent events. During provisioning, step progress and the live log take over the tab. When failed: the failed step, the error, **Retry** and **Retire**, plus **Activate anyway** when only the smoke test failed. That option opens a confirmation dialog showing the proxy test's error.
  - **Health**: verdict sentence, check matrix, state timeline. → [health](../processes/servers/server-health.md)
  - **Stats**: charts, container states. → [stats](../processes/servers/server-stats.md)
  - **Stack**: rendered files (masked), deployment history with diffs.
  - **Jobs**, **Activity**.
- **Redeploy / upgrade / edit parameters**: plan screen (diff, new values, endpoint changes, warnings) → **Apply** → progress. Rolling upgrade: per-server progress table. → [redeploy](../processes/servers/server-redeploy.md)
- **Rotate credentials**: consequences dialog (links affected) → progress. → [rotation](../processes/servers/credential-rotation.md)
- **Retire**: consequences, options, type-the-name. → [retirement](../processes/servers/server-retirement.md)
- **Pause checks**: duration picker.
- **Locations**: a simple table (code, flag, name, servers) with add and edit.
- **Templates list**: name, default version, versions, servers per version, draft marker, archived filter (a chip beside **Active**), **New template** and **Import…** (a zip or a public git repository, into a new template or the draft of an existing one).
- **Template page**: versions (notes, servers using, **Make default**, **View**, **Diff**, **Export**, **New draft from this version**), the draft (files, what changed from its base, where it came from, **Open editor**, **Discard draft…**), and the template's name, slug (fixed after the first version), description, **Archive** and **Delete…** (type the slug; a template any server was built from can only be archived). **Version page**: the files read-only and highlighted, the publish warnings under their lines. **Diff page**: two versions, unified or side by side. **Template editor**: file tree, code editor (CodeMirror, with inline markers), validation panel (per-file errors and warnings with line links), **Preview** (rendered for the sample, with the unsaved edits; for a chosen server once servers exist), **Publish** (a screen with the report, version notes and what will happen) and **Hand off to an agent…**. When another tab or an agent saved the draft first, the editor shows the band "The draft changed since you opened it": its text stays, **Save** and **Publish** are off, **Copy my version** puts every file on the clipboard and **Reload** loads the saved draft. That option opens a dialog: the problem text, the context to include, the agent, how long access lasts, and a preview of the prompt, with **Copy prompt** (no download). While an agent session is open, a band on the editor and the template page shows its saves and expiry, with **Revoke access**. → [template authoring](../processes/servers/template-authoring.md)

### Subscriptions
- **Subscription list**: name, title, server health dots, link count, formats, hide-unhealthy indicator.
- **Subscription page**: a sortable server list (health and "hidden now" per row; **Add servers** multi-select), settings form, a **preview** of the exact output, links of this subscription, activity. → [subscription management](../processes/subscriptions/subscription-management.md)
- **Links list**: filters (subscription, state, expiring, alerts). Columns per [subscriptions module](../modules/subscriptions.md#pages).
- **New link** dialog: name, subscription, expiry, language. It ends on the link page.
- **Link page**: the URL as a secret field with a large **QR**, state and expiry, subscription, the current output preview, the fetch log (time, network, app icon, outcome), alerts, actions (disable, regenerate, change subscription, expiry, cut off, delete) with their consequence dialogs. → [link lifecycle](../processes/subscriptions/link-lifecycle.md)

### Routing
- **Lists**: cards or rows with service count, domain count, targets and their sync state, and default marker.
- **List page**: sortable services (owned / total domains per service), **Add services** (picker with search), targets, warnings (guard refusals, rejected snapshots). → [routing lists](../processes/routing/routing-lists.md)
- **Services**: table with source, tag, counts, last refresh, state, lists. **Add service** (selector field + search) and **New custom service**.
- **Service page**: overview, domains (filter; suffix/exact; owned elsewhere marker), skipped entries, snapshot history with diffs, a rejected-snapshot callout with **Accept anyway** / **Dismiss**. → [service management](../processes/routing/service-management.md)
- **Custom service editor**: domain table, bulk paste with a normalisation preview ("12 added, 2 merged, 1 refused: IP address"), covered-by hints, save.
- **Search**: query, filters, results with **Preview** (a drawer with domains) and **Add to lists…**. Empty result → Discover suggestion. → [catalog search](../processes/routing/catalog-search.md)
- **Discover**: form (website, visit through, depth). The **run page** fills in progressively: suggestions first, then the screenshots and the grouped checklist with class, failure and covered-by columns, then the save actions. → [discovery](../processes/routing/domain-discovery.md)
- **Routers**: table with state, list, last sync result and time, drift and unmanaged indicators.
- **Add router** dialog: fields, Proxier's public key with copyable install snippets, **Test connection** with a fingerprint confirmation step and a checks list.
- **Router page**: connection card (with **Test connection**), installed vs desired per tag, unmanaged tags with **Adopt** / **Remove** / **Ignore**, **Preview sync** (a plan table + scripts), **Sync now**, sync history with plans and logs, activity. → [router sync](../processes/routing/router-sync.md)
- **Shadowrocket**: configs table (list, URL, last fetch). **Config page**: URL secret field + QR, base config editor with versions, rendered **preview**, fetch log. → [Shadowrocket](../processes/routing/shadowrocket-config.md)
- **Import from mtvpn**: a three-step wizard (paste → preview checklist with per-row choices → result). → [mtvpn import](../processes/routing/mtvpn-import.md)

### Router scripts
- **Scripts list**, **script page** (versions, draft editor with the detected-parameters panel beside the text, generations). → [script versions](../processes/router-scripts/script-versions.md)
- **Generate for a new router**: a form built from the parameters (grouped, with descriptions, computed values read-only), a subscription-link section (create / existing / type), a register-for-routing section. → [script generation](../processes/router-scripts/script-generation.md)
- **Generation page**: values (secrets masked), linked link and router, **Download**, **Create fetch URL** (shows the URL with an expiry countdown and the two RouterOS commands to copy), fetch history.

### System
- **Jobs**: filterable table. **Job page**: step progress, live log, payload summary, attempts, **Cancel** / **Retry**, link to the subject. → [jobs](../processes/platform/jobs.md)
- **Activity**: an event stream with filters (module, type, subject, actor, time). Each row reads as a sentence and links to its subject.
- **Settings**: a section navigation as in [platform](../modules/platform.md#settings). The notification toggles are a grouped list with each event's description and default.

## Flows to design first

1. **New server → live provisioning → active** (form, progress, failure, retry).
2. **Server page Health tab**: making "blocked vs down, and why" obvious at a glance.
3. **Dashboard** in three situations: all good, one server blocked, home offline.
4. **New link → share** (copy, QR) and the **link page** with its fetch log.
5. **Subscription page**: server ordering, hide-unhealthy, output preview.
6. **Discover a site → custom service** (the checklist).
7. **Router page with a sync plan preview** and unmanaged tags.
8. **Template editor** with validation results and publish.
9. **Generate a router script → fetch URL**.
10. All of the above at phone width for the "reacting to a notification" case: server page, link page, router page.

## Content rules

- Buttons say what they do ("Rotate credentials", not "OK"). Destructive buttons say the object's name.
- Consequence texts are concrete: "12 links in 3 subscriptions will get new URIs on their next refresh".
- Every verdict, rejection and failure has a one-sentence reason in plain language, with details one click away.
- Secrets are never shown unmasked by default. Revealing is a deliberate click.
- Numbers use the language's formatting, and dates use the display time zone.
