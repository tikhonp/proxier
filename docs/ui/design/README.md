# Design: the Rosé Pine console

This folder holds the final visual design of the Proxier UI. [../README.md](../README.md) says *what* each screen shows and does. This folder says *how it looks and behaves*:

- `tokens.css`: every colour, size and shared class, ready to use as the app's base stylesheet;
- `screens/`: the source of each designed screen, exported from the design canvas;
- this file: the rules, so a new screen can be built without the canvas.

The canvas itself is <https://claude.ai/artifact/TM7E3qaH69dDpV2aY4axec>, on the **Rosé Pine** page. Its other page, **Console**, is an earlier look kept for reference only.

## Principles

1. **Dark only.** Rosé Pine (main) on a black ground, in one typeface, IBM Plex Mono.
2. **Colour means state.** Ink is grey-violet. Colour appears only for a state, and every state is also a shape and a word. Healthy stays quiet: a pine dot with a subtle word.
3. **Big blocks flush, small things padded.** Header, sidebar, areas, title bands, tables, banners and the key line run edge to edge and meet along 1 px `--overlay` lines. Buttons and fields are bordered boxes with room around them.
4. **Less on screen.** One line per thing. The rest is one key or one click away.
5. **Keyboard first on desktop, usable on a phone.** Every action has a key, and the bottom line shows which. The phone is for reacting to a notification.

## Colour and type

All values are in `tokens.css`. Short version:

| Role | Token | Hex | Used for |
|---|---|---|---|
| ground | `--black` | `#000000` | page, areas, bands, dialogs |
| well | `--base` | `#191724` | fields, logs, previews, hover |
| line | `--overlay` | `#26233a` | every 1 px line, the cursor row, the current tab |
| field border | `--hl-med` | `#403d52` | field borders, selection |
| control border | `--hl-high` | `#524f67` | button and dialog borders |
| text | `--text` | `#e0def4` | primary text, key names |
| secondary | `--subtle` | `#908caa` | labels, meta, area titles |
| faint | `--muted` | `#6e6a86` | placeholders, separators, line numbers |

Type sizes:

- Body: 13/20 on desktop, 14/22 on a phone.
- Meta and the key line: 12 px.
- Area titles: 11 px, uppercase, tracked .08em, subtle.
- Page title: 22/28 semibold. Dialog title: 15 px semibold.
- The only weights are 400, 600 and 700 (logo).

Code panes use Rosé Pine syntax colours, matching the user's nvim:

| What | Style |
|---|---|
| keys | foam italic |
| strings | gold |
| punctuation | subtle |
| comments | subtle italic |
| changed values | rose |
| problems | iris with a wavy love underline |

## Status vocabulary

| Shape | Class | Colour | Means | Examples |
|---|---|---|---|---|
| dot | `.st-ok` | pine | works | healthy, synced, succeeded, online, active |
| triangle | `.st-look` | gold | look at it | degraded, drift, looks shared, rejected snapshot, interrupted |
| diamond | `.st-blocked` | iris | works, but not from Russia | blocked |
| square | `.st-broken` | love | broken | down, failed, sync failed, offline |
| ring | `.st-unknown` | subtle | not known yet | unknown, queued, awaiting setup |
| open square | `.st-off` | subtle | switched off | disabled, expired, cancelled |
| bars | `.st-paused` | foam | paused by you | paused |
| pulse | `.st-running` | text | running now | provisioning, syncing, a running job |
| dash | `.st-gone` | muted | gone | retired, deleted |

The marker always comes with its word. For healthy states the word stays subtle; for the others the word takes the state colour.

A serious state on a page gets a **band**: a full-width block in the tinted colours (`--love-bg` / `--love-border` and so on). It holds the state word, since when, one or two plain sentences, and the action buttons. Diffs use `+` foam and `−` love, never colour alone.

## Layout

The desktop layout, from top to bottom:

- **Header** (48 px), made of flush cells that touch each other:
  - the logo (212 px, the same width as the sidebar);
  - the search field, which stretches to fill the rest;
  - the jobs cell;
  - an optional warning cell, such as "Telegram unreachable";
  - the admin menu.
- **Sidebar** (212 px), with grouped navigation. The current page is an overlay row with a `›`.
- **Main area**:
  - the page title block: an `h1`, then one status line in words;
  - optional bands;
  - areas.
- **Areas** each start with a 36 px **title band**: an uppercase label, a short subtle note, and on the right either a `Thing →` link or small buttons. Areas sit side by side in a grid with 1 px gaps on the `--overlay` colour, so the lines come from the gaps and not from borders.
- **Page actions** sit in the title block or the title band, on the right. The one main action is the light (`.pc-pri`) button.
- **Rows** have no dividers. Hover is `--base`; the keyboard cursor row is `--overlay` plus a `›` caret in the left gutter. Every block uses a 16 px side gutter.
- **Counts strip**: equal cells in a grid with 1 px gaps. Each cell holds a marker, a label and a big number. A zero count is muted.
- **Key line** (28 px, desktop only), at the bottom.

## Controls

Every control is a boxed button except those listed here.

| Control | Class | Look |
|---|---|---|
| main action | `.pc-btn.pc-pri` | light fill, dark text, semibold; one per screen |
| ordinary | `.pc-btn` | 1 px `--hl-high` border, transparent |
| filter chip | `.pc-chip` | 26 px toggle in the filter row; colours as the switch: muted border and subtle text when off, light border and light text when on, never filled |
| segmented switch | `.pc-sb` | 24 h / 7 d / 30 d and the like: touching segments in the same off/on colours |
| switch | `.pc-sw` | a square knob filling the track's height, flush at one end; the border is the knob's colour (muted off, light on) |
| destructive | `.pc-btn.pc-dng` | love border and text; names the object and ends in "…" when a dialog follows |
| backing out | `.pc-btn.pc-ghost` | **no border**, same padding; for Cancel and Close |
| small | `+ .pc-sm` | 26 px; at the end of a row or in a title band |
| header cell, tab | `.pc-seg` | flush, no border |
| pop-up close | `.pc-seg.pc-close` | a flush, square × cell in the top-right corner, as tall as the title row, with a 1 px line on its left and no padding |
| field | `.pc-inp` | `--base` well, `--hl-med` border, subtle border on focus, love border with a message on error |
| secret | `.pc-grp` | a masked value plus Reveal · Copy · QR inside one box |

Buttons say what they do. Some buttons start real work even though their label begins with "Cancel" or reads like a dismissal, such as **Cancel provisioning** and **Ignore**. Those keep the ordinary border. A disabled control is the same control at 45 % opacity, with the reason next to it.

## Pop-ups and dialogs

- A pop-up opens on a dimmed page (black at about 70 %), near the top of the screen. Its box has a black fill and a `--hl-high` border.
- **Dialogs** have three parts:
  - a title row: the title, plus the flush × in the corner;
  - a padded body;
  - a footer: the action button first, then a borderless **Cancel**, with key hints on the right.
- Dialogs come in three strengths:
  1. **simple**: `↵` confirms;
  2. **with consequences**: lists what changes, such as how many links and which servers;
  3. **type the name**: for anything that can't be undone. The button stays disabled until the name matches.
- `esc` always closes a pop-up. `⌘↵` submits a form dialog.
- **Search** (`/`) is a pop-up, 680 px wide, near the top. It has no ×: `esc` or a click outside closes it.
  - Results come in one list with two groups: **Go to** (servers, links, routers, pages) and **Actions** (every button in the app, under the same words).
  - `:` opens the same box with actions only. Backspace on an empty box goes back to everything.
  - The footer shows the keys: `↑ ↓` move, `↵` open or run, `:` actions only, `esc` close.
- **Keys** (`?`) is a pop-up listing the whole keymap.

## Keys

The keymap is small and vim-like, "not too crazy". Letters never fire inside a field, and nothing destructive gets a single key.

| Where | Keys |
|---|---|
| Anywhere | `/` search things and actions · `:` actions only · `g` then a letter to go to a page · `?` keys · `esc` close, cancel, leave a field |
| `g` then | `d` dashboard · `s` servers · `t` templates · `u` subscriptions · `l` links · `r` routers · `j` jobs · `a` activity · `g` top of the list. After `g` a which-key strip shows these choices. |
| In a list | `j k` (arrows too) · `gg G` first, last · `↵` open · `n` new · `space` tick |
| Row or page | `r` run checks or retry · `s` sync now · `p` pause or resume · `y` copy the URL or value · `e` edit · `[ ]` previous, next tab |
| Fields and dialogs | `tab` next field · `esc` leave · `⌘↵` save · `↵` confirm a simple dialog |
| Some pages | `J K` move a row where order matters · `f` follow a log · `G` end of the log |
| Template editor | vim keys optional, off by default · `:w` save draft · `]d [d` next, previous problem · `esc esc` leave |

The **key line** at the bottom has three parts:

- **Left:** the keys for whatever is under the cursor, such as "↵ open nl-1 · r run checks now · j k move".
- **Middle:** the global keys `/ : g ?`.
- **Right:** a live indicator.

Short messages ("Copied", "Sync started") appear in the key line, not as toasts.

## Phone

Under 760 px the layout changes:

- The sidebar becomes a drawer behind a ☰ cell, and the key line goes away.
- The header grows to 56 px, with flush cells: ☰, the logo, jobs and search.
- A back row (`‹ Servers`) sits under the header.
- Body text is 14/22. Buttons are at least 48 px tall (small ones 40 px), and actions stack full width.

The phone screens cover the notification flows:

- a blocked server;
- a link to share;
- a failed router sync.

## Screens

The files in `screens/` are the canvas source (`.dc.html`). Use them as a reference, not as finished pages:

- They need the canvas runtime (`support.js`), so they don't open on their own.
- The markup and inline styles are exact. The `<helmet>` block carries the same classes as `tokens.css`.
- The sample data is in the `renderVals()` script at the bottom.

| Area | File | Screen |
|---|---|---|
| Start | `RP-Sign-in` | Sign in: wrong password, lockout, expired session |
| | `RP-Dashboard-first-run` | Dashboard on first run: getting-started checklist, empty states |
| Dashboard | `RP-Main` | One server blocked. Interactive: keys, search pop-up, `?` |
| | `RP-Dashboard-ok` | All good |
| | `RP-Dashboard-offline` | Home offline (verdicts on hold) |
| Servers | `RP-Servers` | Server list: filters, selection, bulk actions |
| | `RP-Server-new` | New server |
| | `RP-Server-provisioning` | Provisioning, live log (`f y G`) |
| | `RP-Server-failed` | Provisioning failed |
| | `RP-Server-overview` | Server Overview tab, with the More menu open |
| | `RP-Server-health` | Health tab, blocked |
| | `RP-Server-stats` | Stats tab: charts with a gap, containers |
| | `RP-Server-stack` | Stack tab: rendered files, deployments |
| | `RP-Server-redeploy` | Upgrade plan: new parameters, file diffs, endpoint change |
| | `RP-Server-rollout` | Rolling upgrade in progress |
| | `RP-Pause-checks` | Pause checks dialog |
| | `RP-Locations` | Locations, adding one inline |
| Templates | `RP-Templates` | Template list |
| | `RP-Template` | Template page: versions, draft, the agent-session band |
| | `RP-Template-editor` | Template editor, draft |
| | `RP-Template-publish` | Publish dialog |
| | `RP-Template-agent` | Hand off the draft to an agent: the prompt and its scoped access |
| Subscriptions | `RP-Subscriptions` | Subscription list |
| | `RP-Subscription` | Subscription page |
| | `RP-Links` | Link list: filters, expiring, alert |
| | `RP-Link-new` | New link dialog |
| | `RP-Link` | Link page with a shared-link alert |
| Routing | `RP-Lists` | Routing lists |
| | `RP-List` | List page right after a reorder, re-syncing |
| | `RP-Services` | Service list |
| | `RP-Service` | Service page with a rejected snapshot |
| | `RP-Custom-service` | Custom service editor with paste-many normalisation |
| | `RP-Search` | Catalog search with the preview drawer |
| | `RP-Discover` | Discover a site → custom service |
| | `RP-Routers` | Router list |
| | `RP-Router-add` | Add router dialog, confirming a host key |
| | `RP-Router` | Router page with a sync preview |
| | `RP-Shadowrocket` | Shadowrocket configs |
| | `RP-Shadowrocket-config` | Config page: URL, rendered output, versions, fetches |
| | `RP-Import` | mtvpn import, preview step |
| Router scripts | `RP-Scripts` | Script list |
| | `RP-Script` | Script page: draft with detected parameters, versions, generations |
| | `RP-Script-generate` | Generate for a new router |
| | `RP-Script-generation` | Generation result, fetch URL |
| System | `RP-Jobs` | Job list |
| | `RP-Job` | Job page: steps, attempts, payload, log |
| | `RP-Activity` | Activity, filtered to one subject |
| | `RP-Settings` | Settings, Notifications section |
| Phone | `RP-Phone-server` / `-link` / `-router` | After a notification |
| Reference | `RP-Vocabulary` | The whole vocabulary: layout, tokens, status, keys, code, controls, dialogs, diffs |
| | `RP-Keys` | The whole keymap |

A few screens are left out because they reuse a drawn layout. These are the server's Jobs and Activity tabs (the job and activity lists, filtered to one server) and the settings sections other than Notifications (the same two-column layout). The remaining wizard steps are left out too: the mtvpn paste and result steps, and the Discover form before a run.

## Changing the design

The canvas is the source of truth until the app exists. To change the design, change the canvas first, then refresh this folder:

1. Ask Claude to "pull the latest design from the canvas".
2. Claude copies the `RP-*.dc.html` files into `screens/`.
3. Claude updates `tokens.css` and this file wherever a rule changed.

Since Phase 0b the app's own stylesheets in `internal/platform/ui/static/css/` (`tokens.css`, `fonts.css`, `app.css`) are the stylesheet of record; this folder's `tokens.css` is the reference copy.
