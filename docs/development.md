# Development

How the code is organised and the rules it follows. What Proxier does is in the [module docs](./modules/) and the [process docs](./processes/README.md); why, in the [ADRs](./adr/). This page is about changing the code without breaking those.

## Making a change

1. Read the process doc of the flow before touching it. Its **Edge cases** are a test list: each one has a test, and a new edge case gets a test with it.
2. Change the docs in the same commit when behaviour changes (process doc, module doc, [events.md](./events.md), [data-model.md](./data-model.md), [ui/README.md](./ui/README.md)). A decision that is hard to reverse, surprising and a real trade-off gets an ADR.
3. Schema changes go only into **new** migrations (`NNNNN_name.sql` in the module's `migrations/`, goose `-- +goose Up` / `Down`, `STRICT` tables). Every module's schema is deployed; never edit an existing migration. Each module's `migrations_test.go` tests the constraints.
4. `make check`, `make lint` (Docker Desktop must be running: `open -a Docker`), `docker build .`.
5. Anything that can only be checked with real hardware, accounts or phones is said so in the commit, not faked. What is still waiting for that is in [exit-demos.md](./exit-demos.md) and [open-questions.md](./open-questions.md#to-verify-on-the-real-deployment).

## Layout

```
cmd/proxier/                 serve | manage <command> | version | healthcheck; modules() lists the enabled modules in order
internal/platform/           the platform, itself module "platform"
  config obs db (+dbtest) vault events settings migrations httpx
  auth web ui pages i18n sitetest
  jobs (+jobstest) notify (+telegram) sshx (+sshxtest) tailnet backup geoip
  module                     Module and the optional interfaces below
internal/modules/servers/        templates, provisioning, deploys, health, stats, retirement, the agent API
internal/modules/subscriptions/  subscriptions, links, the public /s/ fetch, alerts, cut-off
internal/modules/routing/        services, lists, refresh, catalog, Shadowrocket, mtvpn import, router sync, discovery
internal/modules/routerscripts/  script versions, generations, fetch URLs
web/editor/                  source of the CodeMirror bundle (make editor → ui/static/js/editor.bundle.js, committed)
```

Modules are enabled in this order: `servers`, `subscriptions`, `routing`, `routerscripts`; each later one may consume the ports of earlier ones. Inside a module the shape repeats:

| Package | What |
|---|---|
| root | the `module.Module`, its optional interfaces, `New(Ports)`, the ports it provides; wiring only |
| `conf` | the settings section and its keys (a leaf, so services and pages read the same names) |
| `migrations` | the module's schema |
| `store` | every SQL statement of the module, `FieldErrors` (i18n keys), retention constants |
| one package per area | services and their job types (`servers/provision`, `subscriptions/links`, `routing/routers`, `routerscripts/generations`, …) |
| pure packages | no database, no clock of their own: `servers/render`, `subscriptions/output`, `routing/domain`, `selector`, `snapshot`, `own`, `routeros`, `routerscripts/params` |
| `pages` | handlers and templ files |
| `<module>test` | the test harness (below) |

Packages worth knowing by name:

- **servers**: `remote` (every shell command sent to a server, and `remote.Parse`, its inverse), `sealed` (every AAD), `endpoint` (`vless-xhttp-tls`: checks, URI, masked URI), `proxy` (the only importer of xray: proxy test, `Dialer`, config validation), `dns` + `dns/cloudflare`, `checkhost`, `validate` (pure-Go validators), `agent` (`/agent/v1`).
- **routing**: `change` (the sync seam), `sources` (the one outbound HTTP client), `routeros` (every RouterOS command and script, `Parse`/`ParseScript`), `routers` (plan, sync, drift, unmanaged tags, awaiting setup), `discovery` (+ `cdp`, `socks`).
- **routerscripts**: `params` (the PARAMETERS block, literals, `Fill`, `Eval`; `params/paramstest` embeds the real `fresh-router.rsc` at `b1e34f2`, never edited: tests build variants from it in code).

## Module interfaces

All in `internal/platform/module`, found by type assertion; the platform implements the ones it needs like any module. `platform.Open` calls `Init` first and asks for job types, subscribers, renderers and namers after it, so they can point at services built in `Init`.

| Interface | Method |
|---|---|
| `Module` | `Name() string`, `Migrations() fs.FS` |
| `EventDeclarer` | `EventTypes() []events.Type` |
| `SettingsDeclarer` | `SettingsSections() []settings.Section` |
| `Initializer` | `Init(d Deps) error` (`Deps`: Cfg, Log, DB, Vault, Events, Settings, I18n, Auth, Jobs, Dispatcher, Notify, SSH, Tailnet) |
| `Migrated` | `AfterMigrate(ctx) error`, after every migration, on every start |
| `MessagesDeclarer` | `Messages() i18n.Messages` |
| `RouteDeclarer` | `Routes(r web.Routes)`: `r.Admin` (session + CSRF), `r.Public` (no cookies), `r.Shell`, `r.SettingsPages` |
| `NavDeclarer`, `SettingsPageDeclarer`, `IntegrationDeclarer` | sidebar entries, Settings pages, rows on Settings → Integrations |
| `Searcher` | hits for the `/` and `:` pop-up |
| `JobDeclarer` | `JobTypes()`, `Schedules()` |
| `SubscriberDeclarer` | `Subscribers() []events.Subscriber` |
| `SubjectNamer` | names and links for `type:id` subjects in Activity and Jobs |
| `NotificationRenderer` | the module's own notification texts |
| `DashboardDeclarer` | dashboard areas, sorted by `Order` |

## Rules

**Data and transactions**

- `db.DB.Write(ctx, fn)` holds the process's only write connection (`BEGIN IMMEDIATE`). Never touch `d.W` inside its callback (deadlock); reads inside a write use the tx. Nothing slow or remote inside: read, act remotely, then write. `R` is a 4-connection `query_only` pool (`VACUUM INTO` needs its own `mode=ro` connection: `db.DB.VacuumInto`).
- Times are stored as `db.Time` (`2006-01-02T15:04:05.000Z`, SQL default `strftime('%Y-%m-%dT%H:%M:%fZ','now')`) and shown in the display zone.
- A module never reads another module's tables; no foreign keys across modules. Another module's thing is kept by id plus the name copied at the time.

**Ports** (ADR 0002)

- A module's non-test files import from another module only its root package (the ports), plus `servers/endpoint` for subscriptions. `TestImportsOnlyServersPorts` / `TestImportsOnlyPorts` enforce it.
- A nil port hides the feature: no `Rotator` hides Cut off, no `Hostnames` guards nothing, no `Links` / `Routers` turns `@fill` parameters into ordinary fields.
- Ports that write (`LinkIssuer.Issue`, `RouterRegistrar.Register`) run inside the caller's transaction (`links.CreateTx`, `routers.RegisterTx`).

**Events and actors**

- Every state change records its event in the same transaction; a change that changes nothing records nothing. Not events: job lifecycle (the job tables are its history; only `job.failed` and modules' own failure events), notification delivery (only `notification.failed`), bookkeeping (last seen, cursors, schedule runs).
- Actors: `admin`, `system` (reactions, public fetches), `cli`, `job:<id>`, `agent:<session>`. A job's `created_by` adds `schedule:<name>` and `event:<id>`.
- Payloads carry names, not ids, for other things (`list: "Main"`), lists as one comma-joined string, times as `db.Time` strings (`""` for none), and never a secret.
- Every event type needs `event.<type>` messages; a notifying one `notify.<type>` too; every job type `job.<type>` (tests enforce all three).

**Secrets**

- Only through the vault, sealed with an AAD naming where they live, found by `vault.Lookup` (HMAC). AADs: `setting:<key>`, `job:<id>:payload`, `server:<id>:gen:<key>`, `server:<id>:params`, `server:<id>:endpoint:<key>`, `deployment:<id>:…`, `link:<id>:token`, `shadowrocket:<id>:token`, `generation:<id>:secrets`, `fetch_url:<id>:token` (servers' are all in `servers/sealed`).
- Tokens are `vault.NewToken()` (256 bits, 43 characters); agent tokens are `pxa_` + one. A token appears only on its own page (masked, Reveal, Copy, QR), never in an event, notification, log line, job payload, list, search or the dashboard. `httpx.MaskPath` masks `/s/`, `/r/`, `/f/` in logs. Pages that show one are `no-store`.
- A job that renders registers every generated value and secret parameter with `Logger.Redact` before its first remote call.

**Jobs**

- Resource keys: `server:<id>` for every job that changes a server or reads it over SSH (the self-check with `SkipIfBusy`; the proxy test takes none and asks `jobs.System.Busy` instead, since `SkipIfBusy` counts queued jobs too). `router:<id>` for every job that connects to a router. `service:<id>` for one service's refresh.
- Every waiting step selects on `r.Cancelling()`. A job type that writes a deployment marks it failed in `OnCancelled`, and its failure event carries `cancelled: true` (which doesn't notify).
- A server's current files are those of its latest succeeded deployment with `uploaded = 1`, never just the latest deployment.

**Remote text in one place**

- Every shell command sent to a server is built in `servers/remote`; every RouterOS command and script in `routing/routeros`. The fakes answer exactly what `remote.Parse` / `routeros.Parse` recognise, so a command changed in one place changes in the fake too.
- Outbound HTTP of routing goes through `sources.Fetcher` (one client, `proxier/<version>` user agent, a body cap, base URLs in `sources.Endpoints`). Interactive calls: 45 s overall, 15 s per request; jobs: 30 s per request.

**Clocks**: every service that compares times takes `Now func() time.Time`. Each module has `Module.Now`, read through a function set in `Init`, so a test sets it once.

**i18n**: EN and RU catalogs per module (`Messages()`); a key belongs to the module that declares it, under the module's prefixes. `TestEveryUsedKeyExists` scans literals.

## Pages

- `r.Shell` + `ui.Layout`, rendered with `web.Render`. Every button is a `ui.Action` (it shows up in the `:` pop-up); confirmations through `ui.Confirm`; an action that needs fields is a **page styled as a dialog**; secondary actions in an **Actions** area (no "More ▾" menu).
- A strict CSP: no inline `style` or `script` (`TestPagesHaveNoInlineScriptOrStyle`); shared classes go into the platform's `app.css`. Never name a row class `app` (the shell's `.app` is 100 vh).
- htmx 2 doesn't swap 4xx with the platform's config: htmx answers that must re-render are 200 with a notice; plain posts get 409.
- A table is a `.colhead` and its `.row`s inside a `<div class="tbl">` (`TestEveryTableIsInATableBox`). Every column in its `grid-template-columns` has a real minimum (`minmax(120px, 1fr)`, never `minmax(0, …)`: zoomed in, such columns collided). Cells are one line ending in "…", so give a long value a `title`; a message cell that must wrap gets `.wrap`. Two-column page layouts stack in the `max-width: 1100px` block at the end of `app.css`.
- A side panel beside a long form or list is an `aside` that the grid stretches to the row's height (no `align-items: start`), with the divider as its border, and its content in a `<div class="stick">` (sticky at `top: 0`, since the header scrolls away; capped at the window above the key line, scrolling on its own). Stacked under 1100 px, `.stick` is static (`TestSidePanelsStickToTheTop`).
- `proxier.js`: sortable lists (`[data-sortable]`), `[data-cursor]`, `data-copy`, `[data-esc]`, the keymap. Polling with `hx-trigger="every Ns"`, only while something waits.
- CodeMirror lives on the page itself (not a shadow root: Safari's Vimlike took keys typed in it as commands). CodeMirror writes `<style>` tags the CSP blocks, so `web/editor/build.sh` patches style-mod to use adopted stylesheets. Code areas are `textarea[data-code="<mode>"]` (`initCodeAreas()`), so forms work without JS; a page loads the bundle with `Shell.Scripts`.
- After editing `*.templ`, `make templ`; generated files are committed and `make check` compares them against the index (stage them).

## Tests

- Test names are CamelCase sentences (`TestRolledBackChangeLeavesNoEvent`). No test touches the network or sleeps longer than 100 ms; clocks are fake. SQLite in temp files (`dbtest.Open`).
- Harnesses: `sitetest` (the whole app over HTTP, fake auth clock), `jobstest` (fake clock, `Drain`), `sshxtest.Server`, `telegram.Fake`, `serverstest` (fake VPS answering `remote`'s commands, `NewHarness(t, serverstest.StubProxy())`, one VPS per test, 127.0.0.1 only), `proxytest` (in-process xray XHTTP server), `cloudflaretest`, `checkhosttest`, `substest` (`New` on a fake catalog, `WithServers`, `NoRotator`), `routingtest` (`New`, `WithServers`, `WithBrowser`; `h.Router`, `h.Settle`, `h.Advance`), `sourcestest`, `routerostest` (fake RouterOS and jump host), `discoverytest`, `rscriptstest` (`New` on fake ports, `WithModules` with the real ones).
- Test files that use a module harness are external test packages (`package links_test`): the harness imports the module root, which imports every service.
- xray's XHTTP client has a data race (`splithttp.WaitReadCloser.Set`), so packages that push XHTTP traffic (`servers/proxy`, `provision`, `deploy`) run without `-race` (`XRAY_TESTS` in the Makefile). Everything else uses `StubProxy()` and runs under `-race`. This xray has no `allowInsecure`: tests pin the self-signed certificate.
- Never assert the absence of a bare word in a page (CSRF tokens are random): use `>Name<`.
- Move a harness clock only with `Advance`. The display zone in tests is Moscow.
- `TestRealChromium` runs only with `PROXIER_TEST_CHROMIUM_URL` (and `PROXIER_TEST_PAGE_HOST=host.docker.internal` when Chromium is a container).

## Looking in a real browser

Headless Chrome on the host doesn't start; the `chromedp/headless-shell` container does. Run it with `-p 127.0.0.1:19222:9222`, serve the app from a throwaway test or `go run ./cmd/proxier serve` on the host, and drive pages over CDP with `host.docker.internal`. Plain http isn't a secure context there, so stub `navigator.clipboard`. On desktop a click can land on the fixed key line: submit with `⌘↵` or JS. A leftover throwaway instance may hold 127.0.0.1:18099; use another port.
