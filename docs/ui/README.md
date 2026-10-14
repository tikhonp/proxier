# UI: screens, navigation and components

This document lists every screen: what it shows, what can be done there, and the states it handles. How it looks (tokens, rules, screen sources) is in [design/](design/README.md). The UI is built with templ + htmx ([ADR 0014](../adr/0014-server-rendered-ui-templ-htmx.md)). Terms are the glossary's ([CONTEXT.md](../../CONTEXT.md)). Behaviour behind each action is in the linked process docs.

## Character

- **A personal power tool.** One expert user who knows the domain. Dense, but calm, and never cluttered. The design can show IPs, versions and counts without explaining them, but must explain *verdicts* ("why is this blocked?").
- **Status first.** Most visits answer "is everything OK?" in two seconds, then go one level deeper. Health, sync and job states are the most important visual vocabulary.
- **Long work is visible.** Anything that takes more than a second is a job with a live log and step progress, shown where it was started. Nothing spins without saying what it is doing.
- **Safe by default.** Destructive or wide-reaching actions say exactly what will happen (how many links, which servers) and use a preview, a plan or a diff before applying. Irreversible ones need the name typed.
- **Desktop first, phone capable.** Day-to-day work happens on a laptop. The phone is for reacting to a Telegram notification: open the linked page, understand it, pause checks, disable a link, run a sync, copy a link URL or show its QR.
- **Two languages.** English and Russian. Russian strings run about 30 % longer, so no label or button has a fixed width.
- **Dark only**, in Rosé Pine colours (the main variant) on a black ground, with one monospaced typeface, IBM Plex Mono. Colour means state, and every state is also a shape and a word. The full vocabulary is on the [design canvas](https://claude.ai/artifact/TM7E3qaH69dDpV2aY4axec) (Rosé Pine page).
- **Flush blocks, boxed controls.** Big blocks (header, sidebar, areas, tables, banners) run edge to edge and meet along 1 px lines. Buttons and fields keep a border and some room around them, so they always read as controls. There are two exceptions, both for backing out. **Cancel** and **Close** have no border, and a pop-up's **×** fills its top-right corner, flush against the edges like a header cell.
- **Keyboard first on desktop.** A small vim-like keymap: `j`/`k` to move (the arrows and `⌃N`/`⌃P` too), `/` to search things and actions, `:` for actions only, `g` then a letter to go to a page, `?` for the full list. A line at the bottom of every desktop screen shows the keys for what is under the cursor. Letters never fire inside a field, and nothing that deletes has a single key.

## Global layout

- **Sidebar** (collapsible on desktop, a drawer on phone), grouped:
  - **Overview**: Dashboard
  - **Servers**: Servers, Templates
  - **Subscriptions**: Subscriptions, Links
  - **Routing**: Lists, Services, Search, Discover, Routers, Shadowrocket
  - **Router scripts**
  - **System**: Jobs, Activity, Settings
  - under the links, the running build in small muted text (`main-87419a6`, the image's `APP_VERSION`; `dev` for a local build), the whole with "Version" on hover; Settings → General shows it too
- **Header**:
  - **search** (`/`): a pop-up near the top of the screen that finds servers, links, subscriptions, services, routers and router scripts by name, and the actions you can run on them, in one grouped list; `:` opens it with actions only. `↑ ↓` or `⌃N ⌃P` move through the results. It has no ×: `esc` or a click outside closes it. Every button is also an action there, with the same words. On a phone the search is a magnifier cell at the right of the header, and a touch screen's pop-up has no footer of keys;
  - **jobs indicator**: the number of running jobs, opening a list with their current step, and a mark when a job failed since the last look;
  - **notifications health**: a warning if Telegram isn't configured or deliveries are failing;
  - the **member's username** as plain text (a long one, an email, ends in "…" with the whole name on hover, so it never pushes what follows off the screen), then a small **Sign out** button at the end: one click, no confirmation. There is no account menu. **Settings** is in the sidebar (`g ,`) and in the `:` pop-up. Nothing in the header or the `:` pop-up switches the language: it changes only in Settings → General.
- **Key line** at the bottom of every desktop screen: the keys for the row under the cursor on the left, `/ : g ?` on the right. Hidden on a phone. It sticks to the bottom of the window but sits in the page after its content, so at the end of a page nothing is under it. After `g` the which-key strip (the go-to letters, and `g` Top of the list on a page with a list) lies over the page right above the line, which says `g then a key from the list · esc cancel` until a letter, `esc` or 1.8 s ends it; the strip never lengthens the page or covers the line, and a narrow window wraps it upwards.
- **Breadcrumbs** on detail pages. A **page header** with the object's name, its main status badge, and its primary actions. Secondary actions sit in an **Actions** area on the page; there is no "more" menu.

## Status vocabulary

Every status is a marker and a word, never colour alone. The shapes and colours are in the [design](design/README.md#status-vocabulary); blocked must never look like down.

| Thing | States |
|---|---|
| Server health | healthy · degraded · blocked · down · unknown · paused |
| Server lifecycle | provisioning · active · failed · retired |
| Link | active · disabled · expired · deleted, plus a separate marker for a shared-link alert |
| Router | active · awaiting setup · paused, plus the last sync result: synced, syncing, failed, drift |
| Snapshot / service | ok · rejected snapshot waiting · refresh failing |
| Job | queued · running (with the step) · succeeded · failed · cancelled · interrupted |
| Home connectivity | online · foreign unreachable · offline (dashboard banner) |

Time is shown relative ("4 min ago") with the absolute time on hover or tap.

## Shared components

- **Tables** (column heads and rows) keep their columns readable at any zoom: every column has a minimum width, and when the page is narrower (a laptop zoomed to 150 % and more, a small window) the table scrolls sideways in its own box instead of squeezing them. A cell keeps to one line and a long value (a name, a hostname, a template) ends in "…" with the whole value on hover; messages (an error, a reason) wrap between words. On a phone each row stacks. Two-column pages (template, subscription, service, search, script editor, generation, Settings) stack into one column below 1100 px; the template page's versions and draft stack below 1270 px already (a 1440 px window at 125 %), where the versions table would otherwise scroll beside the draft. A version's notes wrap in their own column and stop after three lines with "…" (the version page shows them whole). The template editor keeps its three columns side by side above 1100 px; below, the results go under the file tree and the code, as wide as both with a divider above them, and on a phone everything stacks.
- **Side panels** stay in view: New server's live summary, the generate form's summary, the script's **Detected parameters** and the search preview stick to the top of the window while the form or list beside them scrolls, with their divider the full height of the page part; one taller than the window scrolls on its own. Stacked (below 1100 px) they are part of the page.
- **Notes** under a table or an area (a stack diff's way back, the notes under a subscription's preview) line up with the rows above them: small, subtle text with the rows' side padding, wrapping inside it at any width.
- **Toolbars** never run off the page: filter rows, bulk actions, title actions and breadcrumbs wrap between their items, tabs scroll sideways, and a line of facts ("Netherlands · 10.0.0.1 · nl-1.example.com") wraps between facts, never inside one.
- **Status badge** (above), with an optional "since" and a reason tooltip.
- **Live log viewer**: monospaced, step markers, follow mode, copy and download, redacted secrets shown as `•••`. The log scrolls in its own box, at most 60 % of the window (240 px at least), so what is below it (a server's areas under its provisioning log) is a short page scroll away however long the log is; a wheel over the box scrolls the log, and the page once the log is at its top or end. It opens at its end (the newest lines, where a failure says why) and, while live, follows new lines as long as the reader is at the end: scrolling up to read stops following, and scrolling back to the end, **Follow**, `f` or `G` starts it again. Only the box moves, never the page. **Expand** (offered when the log holds more than it shows) makes the box as tall as the window under its head and brings it to the top; **Collapse** puts it back. On the server page and the job page alike.
- **Step progress**: a vertical list of a job's steps with state and duration. Used on job pages and embedded on server, router and discovery pages.
- **Plan / diff viewer**: file diffs (unified, with a side-by-side option), domain-list diffs (added in green, removed in red, with counts), and per-tag sync plans.
- **Code editor**: YAML, JSON, nginx, shell, RouterOS script, Shadowrocket config; a file tree for templates; inline validation markers. The code runs flush across the page column under a band with **Wrap lines** (long lines wrap instead of scrolling sideways; remembered in the browser, for every editor) and **Full screen** (the header and the sidebar go and the editor takes the window under the page's title, whose actions stay; **Exit full screen** or `esc` returns, the first `esc` leaving the code). On a page whose job is the editor (the template editor, **New config**, **Edit base config**) the editor fills the window under the title down to the key line: the page doesn't scroll, only the code and the side panels do, and **Save** stays in the title block (`⌘S` presses it); a window too short for the code's minimum lets the page scroll. The router script's draft shares its page with versions and generations, so its code is a box at least 78 % of the window tall (as tall as the parameters beside it); in full screen the areas after it go and it fills the window. On a phone the template editor stacks its files, code and results and the page scrolls; its actions go two to a row, and tapping in the code never scrolls the line starts under the line numbers.
- **Secret field**: masked value with **Reveal**, **Copy** and optionally **Regenerate**. Used for connection URIs, link URLs, tokens and settings. Its buttons wrap together, under the value when it leaves them no room, never one of them alone. A URL breaks only after a slash (`…/r/` · token`/` · `iphone.conf`), never inside a word; only a part wider than the line (a token on a phone) breaks where it must.
- **QR code** panel, large enough to scan from a laptop screen. **Show QR** becomes **Hide QR** where it stands and the code opens under it, so toggling moves nothing above the code.
- **Parts of an area** under each other (a URL and its buttons, the note under them, a QR toggle, a fingerprint and its buttons) keep 12 px between them, the area's own padding, so a line of text, a button row and the note after it never touch.
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
Username, password, error area (generic message; lockout countdown), language follows the browser. Once a passkey exists, **Sign in with a passkey** under the password button, and the browser offers passkeys in the username field's autofill; before that the page has no passkey part and loads no passkey script. With the authenticator app on, a right password shows the code step: one numeric field, **Sign in**, **Start over** and the `reset-2fa` hint. → [sign-in](../processes/platform/sign-in.md)

### Dashboard
- Banners: home offline or foreign unreachable; notifications not configured or failing; first-run checklist until done (Cloudflare, Telegram, SSH keys, first server, first subscription and link, first router).
- **Fleet**: counts by state and by health state, only those some server is in (no "0 Failed"), then every server that isn't healthy, at the top with its reason. Servers with "update available".
- **Jobs**: running now, plus failed in the last 24 h.
- **Routing** (after Links, link "Routers →"): "2 routers in sync"; routers with a failing sync ("sync failed at connect · 3 min ago"), drift ("drift: youtube") or unmanaged tags ("1 unmanaged tag: old-work") first, awaiting ones with them; when none, each router with its last sync; **Snapshots** ("None waiting for a decision." or "netflix · 61 % drop · Review"); **Daily refresh** from the last digest ("04:00 · 3 services changed (+42 / −5 domains) · 0 rejected", or "not run yet"). The dashboard has no global headline, so the routers-in-sync count heads the area.
- **Links**: expiring within 7 days; shared-link alerts.
- **Recent activity**: the last 20 events.

### Servers
- **Server list**: see [servers module](../modules/servers.md#server-list). Row click opens the server. Bulk actions. Filter chips. A prominent **New server** button.
- **New server**: one form. IP, root password, country (a searchable picker: type a name in either language or a code and pick "🇳🇱 Netherlands · NL" from the list; preselected from the IP's country until the admin chooses in it), template + version, dynamic parameter fields, notes. A **live summary** shows the name, hostnames, DNS record and endpoint names. After **Create**, it goes straight to the server page with provisioning progress. → [provisioning](../processes/servers/server-provisioning.md)
- **Server page**, with the header (flag, name, health badge with reason, lifecycle state, primary actions **Run checks now** and **Redeploy**; the rest in its Actions area) and tabs:
  - **Overview**: identity, template version, endpoints with connection URIs (secret field + QR), subscriptions containing it, notes, recent events. During provisioning, step progress and the live log take over the tab. When failed: the failed step, the error, **Retry** and **Retire**, plus **Activate anyway** when only the smoke test failed. That option opens a confirmation dialog showing the proxy test's error.
  - **Health**: verdict sentence (none under a healthy verdict, which has no reason), check matrix, state timeline. → [health](../processes/servers/server-health.md)
  - **Stats**: charts, container states. → [stats](../processes/servers/server-stats.md)
  - **Stack**: rendered files (masked), deployment history with diffs.
  - **Jobs**, **Activity**.
- **Redeploy / upgrade / edit parameters**: plan screen (diff, new values, endpoint changes, warnings) → **Apply** → progress. Rolling upgrade: per-server progress table. → [redeploy](../processes/servers/server-redeploy.md)
- **Rotate credentials**: consequences dialog (links affected) → progress. → [rotation](../processes/servers/credential-rotation.md)
- **Retire**: consequences, options, type-the-name. → [retirement](../processes/servers/server-retirement.md)
- **Pause checks**: duration picker.
- **Templates list**: name, default version, versions, servers per version, draft marker, archived filter (a chip beside **Active**), **New template** and **Import…** (a zip or a public git repository, into a new template or the draft of an existing one).
- **Template page**: versions (notes, servers using, **Make default**, **View**, **Diff**, **Export**, **New draft from this version**), the draft (files, what changed from its base, where it came from, **Open editor**, **Discard draft…**), and the template's name, slug (fixed after the first version), description, **Archive** and **Delete…** (type the slug; a template any server was built from can only be archived). **Version page**: the files read-only and highlighted, the publish warnings under their lines. **Diff page**: two versions, unified or side by side. **Template editor**: file tree, code editor (CodeMirror, with inline markers), validation panel (per-file errors and warnings with line links; **Validate** scrolls to it when it is out of the window, as it is under the code below 1100 px, and moves the focus to it), **Preview** (rendered for the sample or a chosen server, with the unsaved edits), **Publish** (a screen with the report, version notes and what will happen) and **Hand off to an agent…**. When another tab or an agent saved the draft first, the editor shows the band "The draft changed since you opened it": its text stays, **Save** and **Publish** are off, **Copy my version** puts every file on the clipboard and **Reload** loads the saved draft. That option opens a dialog: the problem text, the context to include, the agent, how long access lasts, and a preview of the prompt, with a warning that it holds the token, when the access expires and where to revoke it, and **Copy prompt** (no download). While an agent session is open, a band on the editor and the template page shows its saves and expiry, with **Revoke access**. → [template authoring](../processes/servers/template-authoring.md)

### Subscriptions
- **Subscription list**: name, title, server health dots, link count, formats, hide-unhealthy indicator.
- **Subscription page**: a sortable server list (health and "hidden now" per row; **Add servers** multi-select), settings form, a **preview** of the exact output (inside the area's padding, its edges those of the format select and **Reveal** above, long lines wrapped, and its notes under it: masked, base64, hidden servers), links of this subscription, activity. → [subscription management](../processes/subscriptions/subscription-management.md)
- **Links list**: filters (subscription, state, expiring, alerts). Columns per [subscriptions module](../modules/subscriptions.md#pages).
- **New link** dialog: name, subscription, expiry, language. It ends on the link page.
- **Link page**: the URL as a secret field with a large **QR** (the URL, its buttons, its note, the QR's note and **Show QR** each 12 px apart), state and expiry, subscription, the current output preview, the fetch log (time, network, app icon, outcome), alerts, actions (disable, regenerate, change subscription, expiry, cut off, delete) with their consequence dialogs. → [link lifecycle](../processes/subscriptions/link-lifecycle.md)

### Routing
- **Lists**: cards or rows with service count, domain count, targets and their sync state, and default marker. On a phone, where the column heads are hidden, the counts carry their words ("3 services", "120 domains").
- **List page**: sortable services (owned / total domains per service), **Add services** (picker with search; a custom service says "custom" once), targets, warnings (guard refusals, rejected snapshots). → [routing lists](../processes/routing/routing-lists.md)
- **Services**: table with source, tag, counts, last refresh, state, lists. **Add service** (selector field + search) and **New custom service**.
- **Service page**: overview, domains (filter; suffix/exact; installed-elsewhere marker), skipped entries, snapshot history with diffs, a rejected-snapshot callout with **Accept anyway** / **Dismiss**. → [service management](../processes/routing/service-management.md)
- **Custom service editor**: domain table, bulk paste with a normalisation preview ("12 added, 2 merged, 1 refused: IP address"), ownership hints ("not installed in Main: openai has it", "installed; also covered by openai in Main"), save.
- **Search**: query, filters, results with **Preview** (a drawer with domains) and **Add to lists…**. Empty result → Discover suggestion. → [catalog search](../processes/routing/catalog-search.md)
- **Discover**: form (website, visit through, depth), then **Recent runs** (kept 30 days): one row per run with the site, its state and "run #2 · se-1 · 6 h ago", the line going under the site when the page is narrower than 1100 px; a long site ends in "…" with the whole name in a tooltip. The **run page** fills in progressively: suggestions first, then the screenshots and the grouped checklist with class, failure and covered-by columns, then the save actions. → [discovery](../processes/routing/domain-discovery.md)
- **Routers**: table with state, list, last sync result and time, drift and unmanaged indicators.
- **Add router** dialog: fields, Proxier's public key with copyable install snippets, **Test connection** with a fingerprint confirmation step and a checks list.
- **Router page**: connection card (with **Test connection**), installed vs desired per tag, unmanaged tags with **Adopt** / **Remove** / **Ignore**, **Preview sync** (a plan table + scripts; while another job holds the router it says so, links that job and polls every 5 s), **Sync now**, sync history with plans and logs, activity. → [router sync](../processes/routing/router-sync.md)
- **Shadowrocket**: configs table (list, URL, last fetch). **Config page**: the URL secret field with **Show QR** and its note across the page; under it, two columns: **What the phone gets** (the rendered preview, numbered, Proxier's block marked; a long line wraps with its continuation indented under its start; it grows with the page instead of scrolling in a box of its own, so the page has one scrollbar; **Download** sits in its title band) beside **Base config versions** ("v4 · current", the time over the note, which ends in "…" with the whole in its title, then View, Diff and Restore 8 px apart), **Fetches** (one line each: time, address and user agent, a long one ending in "…" with the whole in its title; on a phone the user agent goes under the time and address), **Actions** (the buttons in one column as wide as the longest, the descriptions lined up beside them) and **Activity**. The second column needs 540 px, so below 1270 px the page stacks: versions, fetches, actions and activity first, the preview, which may run thousands of lines, last. **New config** and **Edit base config** are editor pages: **Create config** or **Save** and **Cancel** in the title block, New config's name, routing list and rule policy in a strip over the code, the code across the page filling the window, and a strip under it with **or upload a file**, **or import once from a URL** and the version's **Note**. → [Shadowrocket](../processes/routing/shadowrocket-config.md)
- **Import from mtvpn**: a three-step wizard (paste → preview checklist with per-row choices → result). → [mtvpn import](../processes/routing/mtvpn-import.md)

### Router scripts
- **Scripts list** (name and description, current version, published, generations, draft state; archived behind "Show archived (N)"), **New script** (paste or upload; a page styled as a dialog). → [script versions](../processes/router-scripts/script-versions.md)
- **Script page**: the status line ("current v4 · draft based on v4 · 3 generations"), **Save draft** and **Publish v5…** with a draft, **Edit** without one; areas **Draft** (the CodeMirror editor with RouterOS highlighting, about two thirds, flush beside the **Detected parameters** panel, with **Wrap lines** and **Full screen** in its band: per group its heading, each parameter's name, default as written (`•••` for `@secret`), annotations and description, each computed value with its expression and, when it can be worked out, its value, then the problems with their lines, shown as lint markers in the editor too; one column under 760 px), **Versions** (View, Diff, Download, Make current), **Generations**, **Actions** (Edit details, Discard draft…, Archive / Unarchive, Delete…), **Activity**. A save from a stale tab shows the band "The draft changed since you opened it" with **Reload** and keeps the typed text. **Hand off to an agent…** in the Draft band opens the hand-off page (the problem, the diff option, the agent, how long; then the prompt with the token masked, a warning that it holds the token, when the access expires and where to revoke it, and **Copy prompt**); while a session is open a band over the draft shows the agent, the problem's first line, its saves and expiry, **What it changed** and **Revoke access**, and the page asks every 10 seconds whether the draft changed, showing "The draft changed since you opened it (an agent or another tab saved it)" with Save and Publish off. → [script versions](../processes/router-scripts/script-versions.md#steps-handing-the-draft-to-an-agent) **Publish** (errors, warnings with a tick, notes, what happens), **Version** (the body highlighted with its confirmed warnings under their lines, its parameters, Diff with…, Download, Make current) and **Diff** (any two of the versions and the draft, unified or side by side) are pages.
- **Generate for a new router**: a form built from the parameters (grouped, with descriptions, computed values read-only), a subscription-link section (create / existing / type), a register-for-routing section. → [script generation](../processes/router-scripts/script-generation.md)
- **Generation page**: values (secrets masked), linked link and router, **Download**, **Create fetch URL** (the URL masked with Reveal and Copy, its expiry time and the minutes left, rendered by the server and refreshed while the page polls, and the two RouterOS commands to copy, `y` copies both), After the import (the fetch, Proxier's key, the router's connection and first sync; polls only while something waits), fetch history.

### System
- **Jobs**: filterable table. **Job page**: step progress, live log, payload summary, attempts, **Cancel** / **Retry**, link to the subject. → [jobs](../processes/platform/jobs.md)
- **Activity**: an event stream with filters (module, type, subject, actor, time). Each row reads as a sentence and links to its subject: where the sentence names it ("Created router script Fresh MikroTik"), that name is the link, never a second copy after the sentence. A change is worded by what changed ("Added youtube to routing list Main", not "Changed routing list Main").
- **Settings**: a section navigation as in [platform](../modules/platform.md#settings). **General** starts with the base URL and the running **Version**, both read-only, and ends with **Language**, the admin's own language (a select and Save), the only place it changes. **Security** starts with the ways in: **Passkeys** (each row a name, "synced" when a password manager or keychain holds it, added, last used, and **Remove…**, which opens the current-password field and **Remove passkey** in place; under the list a name and the current password with **Add passkey**, disabled with a note in a browser without passkeys) and **Authenticator app** (On or Off; **Set up…** asks for the password, then shows the QR code, the key as text and the code field with **Turn on** and **Cancel**; when on, the password and **Turn off**). Then Password, Sessions and Lockout. On a phone the sections are a strip of tabs that scrolls sideways, with the current one brought into view and a fade at an end with more. The notification toggles are a grouped list with each event's description and default; each group (a module, with its counts of events and changed ones) folds, and on a phone they start folded, but the one holding the rule just switched.

## Content rules

- Buttons say what they do ("Rotate credentials", not "OK"). Destructive buttons say the object's name.
- Consequence texts are concrete: "12 links in 3 subscriptions will get new URIs on their next refresh".
- Every verdict, rejection and failure has a one-sentence reason in plain language, with details one click away.
- Secrets are never shown unmasked by default. Revealing is a deliberate click.
- Numbers use the language's formatting, and dates use the display time zone.
