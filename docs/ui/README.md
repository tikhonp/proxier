# UI: screens, navigation and components

This document lists every screen: what it shows, what can be done there, and the states it handles. How it looks (tokens, rules, screen sources) is in [design/](design/README.md). The UI is built with templ + htmx ([ADR 0014](../adr/0014-server-rendered-ui-templ-htmx.md)). Terms are the glossary's ([CLAUDE.md](../../CLAUDE.md#glossary)). Behaviour behind each action is in the linked process docs.

## Character

- **A personal power tool.** One expert user who knows the domain. Dense, but calm, and never cluttered. The design can show IPs, versions and counts without explaining them, but must explain *verdicts* ("why is this blocked?").
- **Status first.** Most visits answer "is everything OK?" in two seconds, then go one level deeper. Health, sync and job states are the most important visual vocabulary.
- **Long work is visible.** Anything that takes more than a second is a job with a live log and step progress, shown where it was started. Nothing spins without saying what it is doing.
- **Safe by default.** Destructive or wide-reaching actions say exactly what will happen (how many links, which servers) and use a preview, a plan or a diff before applying. Irreversible ones need the name typed.
- **Desktop first, phone capable.** Day-to-day work happens on a laptop. The phone is for reacting to a Telegram notification: open the linked page, understand it, pause checks, disable a link, run a sync, copy a link URL or show its QR.
- **Two languages.** English and Russian. Russian strings run about 30 % longer, so no label or button has a fixed width.
- **Dark only**, in Rosé Pine colours (the main variant) on a black ground, with one monospaced typeface, IBM Plex Mono. Colour means state, and every state is also a shape and a word. The full vocabulary is on the [design reference](design/README.md).
- **Flush blocks, boxed controls.** Big blocks (header, sidebar, areas, tables, banners) run edge to edge and meet along 1 px lines. Buttons and fields keep a border and some room around them, so they always read as controls. There are two exceptions, both for backing out. **Cancel** and **Close** have no border, and a pop-up's **×** fills its top-right corner, flush against the edges like a header cell.
- **Keyboard first on desktop.** A small vim-like keymap: `j`/`k` to move (the arrows and `⌃N`/`⌃P` too), `/` to search things and actions, `:` for actions only, `g` then a letter to go to a page, `gg`/`G` for the top and bottom, `⌃D`/`⌃U` half a window, `i` into the first field, `e` edit, `x` delete, `esc` to leave a field and then a form through its Cancel, `?` for the full list. A line at the bottom of every desktop screen shows the keys for what is under the cursor. Letters never fire inside a field, and nothing deletes on a single key: `x` only opens the confirmation.

## Global layout

- **Sidebar** (collapsible on desktop, a drawer on phone), grouped:
  - **Overview**: Dashboard
  - **Servers**: Servers, Templates
  - **Subscriptions**: Subscriptions, Links
  - **Routing**: Lists, Services, Search, Discover, Routers, Shadowrocket
  - **Router scripts**
  - **System**: Jobs, Activity, Settings
  - a member sees only the entries of areas their access reaches (Settings is everyone's: General and Security are their own); the `g` letters, the `:` search, the dashboard and Settings' sections follow the same rule
  - in the owned areas a list, the search and the dashboard show only the things the member reads: their own at `own`, everyone's from `view_all` ([ownership](../processes/platform/members.md#ownership)). A thing of another member names its owner on its page ("owner anna") and, when the member may not change it, shows no controls: no actions, settings form, ordering or fetch URL buttons. Pickers (a new link's subscription, a router's list, router scripts' links and routers) offer only what the member may change
  - under the links, the running build in small muted text (`main-87419a6`, the image's `APP_VERSION`; `dev` for a local build), the whole with "Version" on hover; Settings → General shows it too
- **Header**:
  - **search** (`/`): a pop-up near the top of the screen that finds servers, links, subscriptions, services, routers and router scripts by name, and the actions you can run on them, in one grouped list; `:` opens it with actions only. Servers whose name starts with what was typed come first, then those whose name holds it, then those found by country, IP or hostname, the retired last in each. `↑ ↓` or `⌃N ⌃P` move through the results. It has no ×: `esc` or a click outside closes it. Every button is also an action there, with the same words. On a phone the search is a magnifier cell at the right of the header, and a touch screen's pop-up has no footer of keys;
  - **jobs indicator**: the number of running jobs, opening a list with their current step, and a mark when a job failed since the last look;
  - **notifications health**: "Telegram failing" when the member's own latest notification failed;
  - the **member's username** as plain text (a long one, an email, ends in "…" with the whole name on hover, so it never pushes what follows off the screen), then **Sign out** at the end, a square cell with only an icon (its name in the tooltip): one click, no confirmation. There is no account menu. **Settings** is in the sidebar (`g ,`) and in the `:` pop-up. Nothing in the header or the `:` pop-up switches the language: it changes only in Settings → General.
- **Key line** at the bottom of every desktop screen: the keys for the row under the cursor on the left, `/ : g ?` on the right. Hidden on a phone. It sticks to the bottom of the window but sits in the page after its content, so at the end of a page nothing is under it. After `g` the which-key strip (the go-to letters, and `g` Top, of the list or the page) lies over the page right above the line, which says `g then a key from the list · esc cancel` until a letter, `esc` or 1.8 s ends it; the strip never lengthens the page or covers the line, and a narrow window wraps it upwards.
- A page of an area the member's access doesn't reach is a **403 page** naming the area ("Your access to Servers doesn't reach this page"); a button of one, pressed through htmx, says it in the key line instead.
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

- Tables give each column a minimum width. A table wider than its container scrolls horizontally inside that container. Cells use one line with an ellipsis and full text on hover; error and reason cells wrap between words. Rows stack on phones.

  Two-column pages stack below 1100 px. The template page stacks its versions and draft below 1270 px, where the versions table would otherwise need a scrollbar beside the draft. Version notes wrap and stop after three lines; the version page shows them in full.

  The template editor keeps three columns above 1100 px. Below that, results sit under the file tree and code and span both columns, with a divider above. Everything stacks on phones.
- **Side panels** stay in view: New server's live summary, the generate form's summary, the script's **Detected parameters** and the search preview stick to the top of the window while the form or list beside them scrolls, with their divider the full height of the page part; one taller than the window scrolls on its own. Stacked (below 1100 px) they are part of the page.
- **Notes** under a table or an area (a stack diff's way back, the notes under a subscription's preview) line up with the rows above them: small, subtle text with the rows' side padding, wrapping inside it at any width.
- **Toolbars** never run off the page: filter rows, bulk actions, title actions and breadcrumbs wrap between their items, tabs scroll sideways, and a line of facts ("Netherlands · 10.0.0.1 · nl-1.example.com") wraps between facts, never inside one.
- **Status badge** (above), with an optional "since" and a reason tooltip.
- The live log uses monospace text, step markers and redacted secrets shown as `•••`. It supports follow, copy and download. The log box starts between 240 px and 60% of the window height. A wheel scrolls the box, then the page when the box reaches an end.

  The viewer opens at the newest lines. While live, it follows new lines only when the reader is at the end. Scrolling up stops follow; scrolling to the end, **Follow**, `f` or `G` resumes it. Following moves only the log box. **Expand** appears when content overflows and fills the window below the log heading. **Collapse** restores the initial size. Server and job pages use the same viewer.
- **Step progress**: a vertical list of a job's steps with state and duration. Used on job pages and embedded on server, router and discovery pages.
- **Plan / diff viewer**: file diffs (unified, with a side-by-side option), domain-list diffs (added in green, removed in red, with counts), and per-tag sync plans.
- The code editor supports YAML, JSON, nginx, shell, RouterOS and Shadowrocket syntax, with inline validation markers. Templates also have a file tree. **Wrap lines** keeps a browser-wide preference for wrapping long lines. **Full screen** hides the header and sidebar while keeping the page title and actions. **Exit full screen** or `esc` returns; the first `esc` leaves the code editor.

  The template editor, **New config** and **Edit base config** fill the window between the title and key line. Only the code and side panels scroll. **Save** stays in the title and responds to `⌘S`. The page can scroll if the window is shorter than the editor's minimum height.

  A router script draft shares its page with versions and generations. Its editor is at least 78% of the window height and matches the parameter panel. Full screen hides the following areas and fills the window. On phones, template files, code and results stack, actions use two columns, and the page scrolls. Focusing the code must not move line starts under the line numbers.
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
- Banners: home offline or foreign unreachable; notifications not configured (no bot, to `settings: manage`), "Your notifications have nowhere to go" (a bot, but the member has no chat; **Choose chat**), the member's own failed notifications with **Retry**; first-run checklist until done (Cloudflare, Telegram, SSH keys, hostname pattern, first server, first subscription and link, first router).
- **Fleet**: counts by state and by health state, only those some server is in (no "0 Failed"), then every server that isn't healthy, at the top with its reason. Servers with "update available".
- **Jobs**: running now, plus failed in the last 24 h.
- **Routing** (after Links, link "Routers →"): "2 routers in sync"; routers with a failing sync ("sync failed at connect · 3 min ago"), drift ("drift: youtube") or unmanaged tags ("1 unmanaged tag: old-work") first, awaiting ones with them; when none, each router with its last sync; **Snapshots** ("None waiting for a decision." or "netflix · 61 % drop · Review"); **Daily refresh** from the last digest ("04:00 · 3 services changed (+42 / −5 domains) · 0 rejected", or "not run yet"). The dashboard has no global headline, so the routers-in-sync count heads the area.
- **Links**: expiring within 7 days; shared-link alerts.
- **Recent activity**: the last 20 events.

### Servers
- **Server list**: see [servers module](../modules/servers.md#server-list). Row click opens the server. Bulk actions. Filter chips. A prominent **New server** button. A member at `servers: view` gets a plain list of the servers in service instead: name, country, health state, nothing to open.
- The New server form accepts the server's IPv4 address, SSH port, access mode, setup mode, country, optional hostname, optional additional addresses (IP and hostname rows, added and removed in place), template version, parameters and notes. Advanced IP configuration is a collapsed section under the IP field. It holds the optional SSH connection IP, an IPv4 or IPv6 address that SSH uses instead of the server's IP, for example a Tailscale address. The section opens when the field has a value or an error. The field does not open a numeric keypad on phones, so IPv6 colons can be typed. The SSH access selector shows either the root password field or the SSH account field. Setup options contains the setup mode and SSH port. It starts collapsed for Full setup on port 22 and opens for other values or validation errors. Its summary shows the current setup mode. Root password access creates the `proxier` account. SSH key access uses an account that already accepts Proxier's key. Full setup hardens SSH and configures the firewall and TCP BBR. Deploy only installs prerequisites and Docker. The country picker accepts a name in English or Russian or a country code. It follows the IP's suggested country until the member changes it. The live summary shows the server name, hostnames, DNS record, additional addresses and endpoint names. It shows the SSH connection IP only when one is set and differs from the server's IP. For a hostname outside configured Cloudflare zones, it shows the record to create and a Check the record button. Skip DNS check appears beside that record in the live summary. It skips the form check and provisioning wait. The Check the record button is disabled while the bypass is selected. After Create, the server page shows provisioning progress. See [provisioning](../processes/servers/server-provisioning.md).
- **Server page**, with the header (flag, name, health badge with reason, lifecycle state, the SSH connection IP on its own line when one is set, primary actions **Run checks now** and **Redeploy**; the rest in its Actions area) and tabs:
  - **Overview**: identity (with the previous IPs and their state, and the additional addresses), template version, endpoints with connection URIs (secret field + QR), subscriptions containing it, notes, recent events. The additional addresses appear as IP and hostname lines in the identity block. **Addresses…** sits in the Actions area, beside Change IP, and opens the Addresses page. During provisioning, step progress and the live log take over the tab. When failed: the failed step, the error, **Retry** and **Retire**, plus **Activate anyway** when only the smoke test failed. That option opens a confirmation dialog showing the proxy test's error.
  - **Addresses page** (`/servers/<id>/addresses`, reached from the Actions area): lists the additional addresses, each as IP and hostname, and a short note that their DNS records are the member's. Only the last address has a **Remove** button, which asks for confirmation, because endpoints bind to addresses by position. Below the list, a form adds an address with an IPv4 and a full hostname. Each add or remove redeploys the server and returns to the page with a notice that the addresses take effect when the redeploy finishes. When the change is saved but the redeploy cannot start, the page shows a warning with the reason and a **Redeploy** link instead, and the change stays saved. → [redeploy](../processes/servers/server-redeploy.md)
  - **Health**: verdict sentence (none under a healthy verdict, which has no reason), check matrix, the previous IPs when there are any (state, latest result, 24 h strip, **Switch back**, **Forget**), state timeline. → [health](../processes/servers/server-health.md), [previous IPs](../processes/servers/ip-change.md#steps-previous-ips)
  - **Stats**: charts, container states. → [stats](../processes/servers/server-stats.md)
  - **Stack**: rendered files (masked), deployment history with diffs.
  - **Jobs**, **Activity**.
- **Redeploy / upgrade / edit parameters**: plan screen (diff, new values, endpoint changes, warnings) → **Apply** → progress. Rolling upgrade: per-server progress table. → [redeploy](../processes/servers/server-redeploy.md)
- **Rotate credentials**: consequences dialog (links affected) → progress. → [rotation](../processes/servers/credential-rotation.md)
- **Change IP**: a page styled as a dialog: what to set up at the provider first (for a record made by hand, pointing it at the new IP), what the job does and the records it repoints, a band when the stack uses the IP, the new IP (suggesting previous IPs), **Keep the old IP** (checked) and **Switch anyway** → the job's steps and log on the server page. **Switch back** opens it with a previous IP filled in. → [IP change](../processes/servers/ip-change.md)
- **Retire**: consequences (the DNS records it deletes, or that it leaves a record made by hand), options, type-the-name. → [retirement](../processes/servers/server-retirement.md)
- **Pause checks**: duration picker.
- **Templates list**: name, default version, versions, servers per version, draft marker, archived filter (a chip beside **Active**), **New template** and **Import…** (a zip or a public git repository, into a new template or the draft of an existing one).
- The template page lists versions, the draft and template details. Version actions include **Make default**, **View**, **Diff**, **Export** and **New draft from this version**. Each version shows notes and the servers using it. The draft shows its files, base comparison and origin, with **Open editor** and **Discard draft…**. The slug becomes fixed after the first version. Deletion requires typing it, and a template used by any server can only be archived.

  The version page shows highlighted read-only files with publish warnings below the affected lines. The diff page compares two versions in unified or side-by-side mode.

  The template editor has a file tree, CodeMirror editor and validation panel. **Validate** links errors and warnings to lines, scrolls the results into view and focuses them. Below 1100 px the results sit under the code. **Preview** renders unsaved edits for a sample or chosen server. **Publish** opens the report, version notes and expected effects.

  If another tab or agent saved first, a warning says "The draft changed since you opened it". The editor keeps the typed text and disables **Save** and **Publish**. **Copy my version** copies every file, and **Reload** loads the saved draft.

  **Hand off to an agent…** opens a dialog for the problem, context, agent and access duration. The prompt preview warns that it contains a token and states its expiry and revocation location. **Copy prompt** copies it. While access is open, the editor and template page show saves, expiry and **Revoke access**. See [template authoring](../processes/servers/template-authoring.md).

### Subscriptions
- **Subscription list**: name, title, server health dots, link count, formats, hide-unhealthy indicator.
- **Subscription page**: a sortable server list (health and "hidden now" per row; **Add servers** multi-select), settings form, a **preview** of the exact output (inside the area's padding, its edges those of the format select and **Reveal** above, long lines wrapped, and its notes under it: masked, base64, hidden servers), links of this subscription, activity. → [subscription management](../processes/subscriptions/subscription-management.md)
- **Links list**: filters (subscription, state, expiring, alerts). Columns per [subscriptions module](../modules/subscriptions.md#pages).
- **New link** dialog: name, subscription, expiry, language. It ends on the link page.
- The public link page uses a standalone layout with the logo bar and `linkpage.js`. It has no application shell, keymap, htmx or `proxier.js`. The page shows the subscription title, expiry and import instructions. A subscription with no servers gets a gold notice.

  Platform tabs use two rows of three on phones and one row on computers. JavaScript selects the detected platform; without JavaScript, every section appears under its heading. App cards show a name, note and three steps covering download, import and connection. The first app has the **Recommended** badge and a filled background. Its **Add to {app}** button is the primary action and fills the width on phones, with a copy fallback below it.

  **Your link** sits beside the apps on computers and below them on phones. The URL is selectable in one tap. The area includes **Copy link**, the QR code, an automatic-update note and the contact. The QR starts expanded on computers and collapsed on phones. A disabled, expired or deleted link shows only its message and contact. See [browser page](../integrations/subscription-format.md#browser-page).
- **Link page**: the URL as a secret field with a large **QR** (the URL, its buttons, its note, the QR's note and **Show QR** each 12 px apart), state and expiry, subscription, the current output preview, the fetch log (time, network, app icon, outcome), alerts, actions (disable, regenerate, change subscription, expiry, cut off, delete) with their consequence dialogs. **Cut off** shows only with `servers: manage`. → [link lifecycle](../processes/subscriptions/link-lifecycle.md)

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
- Shadowrocket lists configs with their routing list, URL and last fetch. A config page starts with the secret URL field, **Show QR** and its note across the page.

  Below it, **What the phone gets** shows the numbered rendered preview and marks Proxier's block. Long lines wrap with indented continuations. The preview grows with the page, which has one scrollbar. **Download** sits in its title band.

  The second column contains **Base config versions**, **Fetches**, **Actions** and **Activity**. Version rows show the current marker, time, truncated note with the full note in its title, and View, Diff and Restore buttons 8 px apart. Fetches show time, address and user agent, with a title for truncated text. On phones, the user agent sits below the time and address. Action buttons share a column sized to the longest label, with descriptions aligned beside them.

  The second column needs 540 px. Below 1270 px the page stacks it before the potentially long preview. **Edit base config** and **Hand off to an agent…** sit in the title. An active session shows a band on the config and editor pages.

  **New config** and **Edit base config** fill the window with code. Their title holds **Create config** or **Save**, plus **Cancel**. New config puts name, list and policy above the code. Upload, URL import and the version note sit below it. The hand-off page collects the problem, agent and duration, identifies the values hidden from the agent, and warns that each save reaches the phone. It shows a masked prompt and **Copy prompt**. See [Shadowrocket](../processes/routing/shadowrocket-config.md).
- **Import from mtvpn**: a three-step wizard (paste → preview checklist with per-row choices → result). → [mtvpn import](../processes/routing/mtvpn-import.md)

### Router scripts
- **Scripts list** (name and description, current version, published, generations, draft state; archived behind "Show archived (N)"), **New script** (paste or upload; a page styled as a dialog). → [script versions](../processes/router-scripts/script-versions.md)
- The script page shows current version, draft base and generation count. With a draft it offers **Save draft** and **Publish v5…**; without one it offers **Edit**.

  **Draft** places the RouterOS editor beside **Detected parameters**, with the code using about two thirds of the width. The editor has **Wrap lines**, **Full screen** and lint markers. Parameters appear by group with name, default, annotations and description. Secret defaults show `•••`. Computed values show their expressions and evaluated values when available. Problems identify their lines. Below 760 px the areas stack.

  The remaining areas are **Versions**, **Generations**, **Actions** and **Activity**. Version actions include View, Diff, Download and Make current. Script actions include Edit details, Discard draft, Archive, Unarchive and Delete. Publishing shows errors, warnings requiring acknowledgement, notes and effects. A version page shows highlighted source, confirmed warnings, parameters and version actions. The diff page compares any two versions or the draft in unified or side-by-side mode.

  A stale save keeps the editor text and shows "The draft changed since you opened it" with **Reload**. **Hand off to an agent…** collects the problem, diff option, agent and duration. The prompt masks its token and states token exposure, expiry and where to revoke access, with **Copy prompt**.

  While a session is open, a band shows the agent, first problem line, saves and expiry, plus **What it changed** and **Revoke access**. The page checks for changes every 10 seconds. A changed draft disables Save and Publish and offers Reload. See [script versions](../processes/router-scripts/script-versions.md#steps-handing-the-draft-to-an-agent).
- **Generate for a new router**: a form built from the parameters (grouped, with descriptions, computed values read-only), a subscription-link section (create / existing / type), a register-for-routing section. → [script generation](../processes/router-scripts/script-generation.md)
- **Generation page**: values (secrets masked), linked link and router, **Download**, **Create fetch URL** (the URL masked with Reveal and Copy, its expiry time and the minutes left, rendered by the server and refreshed while the page polls, and the two RouterOS commands to copy, `y` copies both), After the import (the fetch, Proxier's key, the router's connection and first sync; polls only while something waits), fetch history.

### System
- **Jobs**: filterable table. **Job page**: step progress, live log, payload summary, attempts, **Cancel** / **Retry**, link to the subject. → [jobs](../processes/platform/jobs.md)
- **Activity**: an event stream with filters (module, type, subject, actor, time). Each row reads as a sentence and links to its subject: where the sentence names it ("Created router script Fresh MikroTik"), that name is the link, never a second copy after the sentence. A change is worded by what changed ("Added youtube to routing list Main", not "Changed routing list Main").
- Settings uses [section navigation](../modules/platform.md#settings). On phones, sections form a horizontally scrolling tab strip. The active tab scrolls into view, and fades indicate more tabs.

  **Members** lists username, access preset or Custom, and state. Waiting members show setup-link expiry. Removed members have a separate table. **Add member** takes a username, levels per area and the adder's password. Owner, Operator and Viewer fill the level fields; levels above the adder's own are disabled.

  A member page shows their access, setup link while waiting and **Remove member**. Members cannot edit their own levels, and the page explains why. **Copy link** copies the setup URL, which is visible only immediately after creation. **New setup link…** opens the password field in place. The setup page asks for the new password twice. Used, expired or unknown links say "This link doesn't work". A browser already signed in offers **Sign out** first.

  **General** shows read-only base URL and running **Version**. Its **Language** selector changes the member's language and is the only place to do so.

  **Security** starts with passkeys and the authenticator app, followed by Password, Sessions and Lockout. A passkey row shows name, synced status when applicable, addition time and last use. **Remove…** asks for the current password in place. **Add passkey** takes a name and password; unsupported browsers get a disabled action and explanation. Authenticator setup asks for the password, then shows a QR code, text key, code field, **Turn on** and **Cancel**. Turning it off requires the password.

  **Notifications** starts with the member's Telegram chat or "No chat yet." **Detect chat** lists chats that contacted the bot, excluding other members' chats. A member can also type an ID, **Save chat** and **Send test**. Without a bot, a notice links settings managers to its setup page. **Integrations → Telegram** holds the bot token.

  Notification toggles include only events eligible for that member, grouped by module with event and changed-setting counts. Each toggle shows its description and default. Groups collapse; on phones they start collapsed except the group containing the rule just changed.

## Content rules

- Buttons say what they do ("Rotate credentials", not "OK"). Destructive buttons say the object's name.
- Consequence texts are concrete: "12 links in 3 subscriptions will get new URIs on their next refresh".
- Every verdict, rejection and failure has a one-sentence reason in plain language, with details one click away.
- Secrets are never shown unmasked by default. Revealing is a deliberate click.
- Numbers use the language's formatting, and dates use the display time zone.
