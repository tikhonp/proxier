# Build contracts

Phase 0 ([roadmap](../roadmap.md#phase-0-platform-skeleton)) is built in sub-phases, one per fresh context. Each sub-phase has a **contract** here, written before it starts:

| Sub-phase | Contract | State |
|---|---|---|
| 0a Skeleton | (this file, [Built in 0a](#built-in-0a)) | done 2026-10-07 |
| 0b Sign-in + UI shell | [0b.md](./0b.md) | done 2026-10-07 |
| 0c Jobs + events dispatch | [0c.md](./0c.md) | done 2026-10-07 |
| 0d Telegram notifications | [0d.md](./0d.md) | done 2026-10-07 |
| 0e SSH, tailnet, backups, deploy | [0e.md](./0e.md) | done 2026-10-07 (the exit demo on the real deployment is the user's) |

Phase 1 ([roadmap](../roadmap.md#phase-1-servers)) is built the same way. Its cross-cutting decisions are in [Phase 1](#phase-1-servers) below.

| Sub-phase | Contract | State |
|---|---|---|
| 1a Servers module, tables, templates core | [1a.md](./1a.md) | done 2026-10-07 |
| 1b Template UI, versions, import/export | [1b.md](./1b.md) | done 2026-10-07 |
| 1c Cloudflare, embedded xray, endpoints | [1c.md](./1c.md) | done 2026-10-07 |
| 1d Provisioning and the server page | [1d.md](./1d.md) | done 2026-10-08 (the real-VPS run is the user's) |
| 1e Redeploy, upgrade, rollout, rotation | [1e.md](./1e.md) | done 2026-10-08 (real-VPS and browser checks are the user's) |
| 1f Health and stats | [1f.md](./1f.md) | done 2026-10-08 (real-VPS, real-browser and provisioned-server check-host runs are the user's) |
| 1g Retirement, server list, Phase 1 exit | [1g.md](./1g.md) | done 2026-10-08 (the exit demo, real-VPS and real-browser runs are the user's) |
| 1h Agent hand-off | [1h.md](./1h.md) | done 2026-10-08 (a real agent run and a real-browser look are the user's) |

The order is a dependency order: each builds only on finished ones. 1h was not needed for the Phase 1 exit demo (1g) and came after it.

Phase 2 ([roadmap](../roadmap.md#phase-2-subscriptions)) is built the same way. Its cross-cutting decisions are in [Phase 2](#phase-2-subscriptions) below.

| Sub-phase | Contract | State |
|---|---|---|
| 2a Subscriptions module, schema, subscriptions | [2a.md](./2a.md) | done 2026-10-08 (real phones and apps are the user's) |
| 2b Links and the public fetch | [2b.md](./2b.md) | done 2026-10-08 (real phones and apps are the user's) |
| 2c Expiry, shared-link alerts, dashboard | [2c.md](./2c.md) | done 2026-10-08 (real apps, real ipinfo and Telegram delivery are the user's) |
| 2d Cut-off, Phase 2 exit | [2d.md](./2d.md) | done 2026-10-08 (the exit demo, open questions 1 and 2 and the gateway deploy are the user's) |

Phase 3 ([roadmap](../roadmap.md#phase-3-routing)) is built the same way. Its cross-cutting decisions are in [Phase 3](#phase-3-routing) below.

| Sub-phase | Contract | State |
|---|---|---|
| 3a Routing module, schema, sources, services | [3a.md](./3a.md) | done 2026-10-09 (real upstreams and a real-browser look are the user's) |
| 3b Routing lists, ownership, server-hostname guard | [3b.md](./3b.md) | done 2026-10-09 (a real-browser look is the user's) |
| 3c Upstream refresh, catalog, search | [3c.md](./3c.md) | done 2026-10-09 (real upstreams and a real-browser look are the user's) |
| 3d Shadowrocket config, mtvpn import | [3d.md](./3d.md) | done 2026-10-09 (real phones, a real-browser look at the editor and the real mtvpn.yaml import are the user's) |
| 3e Router sync | [3e.md](./3e.md) | done 2026-10-09 (a real router, jump host and tailnet, and a real-browser look are the user's) |
| 3f Drift, unmanaged tags, router lifecycle, dashboard | [3f.md](./3f.md) | done 2026-10-09 (a real router and router script, a real-browser look and Telegram delivery are the user's) |
| 3g Discovery, Phase 3 exit | [3g.md](./3g.md) | done 2026-10-09 (the exit demo, the sh-blackberry draft and a visit through a real server are the user's) |

3d (Shadowrocket and the import) comes before router sync on purpose: neither has a remote side, so after 3d the admin can import `mtvpn.yaml` and move the phone off copyparty before the riskiest part, RouterOS, is built.

A contract lists the files to create, the table definitions, the Go signatures other code will call, the decisions already taken, and a checklist that maps every edge case of the process docs (plus the contract's own decisions) to a test name.

## Tables are already built

On 2026-10-07 the four Phase 0 migrations were written ahead of their sub-phases, with every table decision settled in the contracts: `00002_auth.sql` (0b), `00003_jobs.sql` (0c), `00004_notifications.sql` (0d), `00005_ssh.sql` (0e), and `internal/platform/migrations/migrations_test.go` for their constraints. A sub-phase does **not** create its migration again; it builds on the table. Until the first production deploy (0e) nothing depends on them, so a sub-phase that needs a change edits its migration in place (and the contract's **Tables** and `migrations_test.go` with it). After that deploy, changes go only into **new** migrations (`00006_…`). Their contract's **Tables** section is the description of what is there.

## How to use a contract

1. Read the contract, then the docs it names. The contract wins over the docs where they differ; the docs are updated in the same commit (each contract has a **Doc changes** list).
2. Build to the signatures. A signature may change while building if the reason is real. Write the change into the contract, because the next contracts build on it.
3. Tick the checklist. Every line is a test with that name, in that file. A test may cover more than its line; a line may not go untested.
4. At the end: `make check`, `make lint`, `docker build .`, then mark the sub-phase done in the table above and add an **As built** section to the contract with the deviations and notes for the next sub-phase. Commit.

## Conventions every contract follows

- **Test names** are sentences in CamelCase, like 0a's: `TestRolledBackChangeLeavesNoEvent`. No underscores. Table-driven where the cases share one shape.
- **Clocks.** Every service that compares times takes `Now func() time.Time` (default `time.Now`). Tests move a fake clock; no test sleeps longer than 100 ms.
- **Remote services in tests** are `httptest` servers or in-process fakes (an SSH server on `x/crypto/ssh`, a Telegram Bot API fake). No test touches the network.
- **Events.** Every state change records its event in the same transaction, with these exceptions, decided here once:
  - Job lifecycle (queued, running, succeeded, cancelled, interrupted, retried) lives in the job tables, which are its history. Only `job.failed` and the modules' own failure events are events.
  - Notification delivery lives in the notifications table. Only `notification.failed` is an event.
  - Bookkeeping that changes no behaviour records nothing: a session's last-seen time and IP, `admin.jobs_seen_at`, a known host's last use, schedule run times and skips, subscriber cursors.
- **Actors** are `admin`, `system`, `cli`, `job:<id>`. A job's `created_by` adds `schedule:<name>` and `event:<id>`.
- **i18n keys** belong to the module that declares them. The platform's prefixes: `ui.`, `nav.`, `auth.`, `settings.`, `dash.`, `search.`, `keys.`, `jobs.`, `job.`, `activity.`, `event.`, `notify.`, `ssh.`, `tailnet.`, `backup.`, `err.`.
- **Routes** are added by the module that owns them, through `module.RouteDeclarer` (from 0b). Admin routes sit under the session and CSRF middleware; public routes (`/s/`, `/r/`, `/f/`) never touch cookies.

## Module interfaces, by the sub-phase that adds them

All live in `internal/platform/module`. The platform finds them by type assertion, as 0a does for events and settings. The platform itself (module `platform`) implements each one it needs, like any module.

| Interface | Added in | Method |
|---|---|---|
| `Module` | 0a | `Name() string`, `Migrations() fs.FS` |
| `EventDeclarer` | 0a | `EventTypes() []events.Type` |
| `SettingsDeclarer` | 0a | `SettingsSections() []settings.Section` |
| `Initializer` | 0b | `Init(d Deps) error`, called by `platform.Open` after the platform services exist |
| `MessagesDeclarer` | 0b | `Messages() i18n.Messages` |
| `RouteDeclarer` | 0b | `Routes(r web.Routes)` |
| `NavDeclarer` | 0b | `Nav() []ui.NavItem` |
| `SettingsPageDeclarer` | 0b | `SettingsPages() []ui.SettingsPage` |
| `Searcher` | 0b | `Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error)` |
| `JobDeclarer` | 0c | `JobTypes() []jobs.Type`, `Schedules() []jobs.Schedule` |
| `SubscriberDeclarer` | 0c | `Subscribers() []events.Subscriber` |
| `SubjectNamer` | 0c | `SubjectTypes() []string`, `NameSubjects(ctx, typ string, ids []string) (map[string]ui.SubjectRef, error)` |
| `NotificationRenderer` | 0d | `RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error)` |
| `Migrated` | 1a | `AfterMigrate(ctx context.Context) error` |
| `DashboardDeclarer` | 1d | `Dashboard(ctx context.Context) ([]ui.DashboardArea, error)` |

`module.Deps` grows with the sub-phases: `Cfg, Log, DB, Vault, Events, Settings, I18n` (0b), `Auth` (0b), `Jobs, Dispatcher` (0c), `Notify` (0d), `SSH, Tailnet` (0e).

## Events added to the catalog in Phase 0

Not in [events.md](../events.md) yet; each sub-phase adds its own rows there in its commit.

| Event | Sub-phase | Subject | Payload | Notify |
|---|---|---|---|---|
| `admin.language_changed` | 0b | admin | from, to | off |
| `auth.signed_out` | 0b | admin | session (id), by (`self` / `settings`) | off |
| `schedule.enabled_changed` | 0c | schedule (`schedule:<name>`) | name, enabled | off |
| `notification.failed` | 0d | event (`event:<id>`) | channel, error | off, and never notifiable |
| `ssh.key_generated` | 0e | ssh identity (`ssh:identity`) | fingerprint, regenerated | **on** when regenerated |
| `ssh.host_key_pinned` | 0e | known host (`ssh_host:<address>`) | address, fingerprint | off |
| `ssh.host_forgotten` | 0e | known host (`ssh_host:<address>`) | address, fingerprint | off |

## Built in 0a

What the contracts rely on (details in the code and its tests):

- `platform.Open(cfg, log, modules...)` validates module names, opens the DB, declares event types and settings sections. `App.Migrate` runs the platform's migrations, then each module's, each with its own `<module>_goose_db_version` table.
- `db.DB{W, R}`: `W` is the only write connection (`BEGIN IMMEDIATE`), used through `d.Write(ctx, fn)`; never touch `d.W` inside the callback. `R` is a 4-connection `query_only` pool. `db.Time` stores `2006-01-02T15:04:05.000Z`. Tables are `STRICT`.
- `vault`: `Seal/Open(…, aad)`, `Lookup(secret) []byte` (HMAC), `NewToken()` (43 chars, 256 bits).
- `events.Catalog`: `Declare`, `Record(ctx, tx, Event)` (refuses undeclared types and a missing actor), `After(ctx, q, id, limit)`.
- `settings.Store`: `Register`, `Lookup/Get/GetInt/GetBool/GetDuration`, `Set(ctx, actor, section, values)` returning `settings.FieldErrors`; a value equal to its default deletes its row; `settings.changed{keys}` only when something changed.
- `httpx.New`: our own `IPExtractor` (X-Real-IP only from a trusted peer), request log, Recover, `/healthz`.

## Phase 1: Servers

Decided once for every Phase 1 contract (2026-10-07). A contract may narrow these; it may not contradict them without writing the change here.

### Layout

One module, `servers` (`internal/modules/servers`), enabled in `modules()` in `cmd/proxier/main.go` from 1a. Its packages:

| Package | What | From |
|---|---|---|
| `servers` | the `module.Module` and every optional interface; wiring only | 1a |
| `servers/store` | every SQL statement of the module (locations, templates, servers, endpoints, deployments, DNS records, results, samples) | 1a, grows |
| `servers/finding` | `Severity`, `Finding`, `Report`: the leaf package every checker returns (breaks the manifest ↔ validate ↔ render cycle) | 1a |
| `servers/manifest` | `manifest.yaml` types, parsing, schema checks | 1a |
| `servers/render` | the rendering context, the function map, rendering a version for a server | 1a |
| `servers/gen` | generated values | 1a |
| `servers/validate` | the validators and the report | 1a (xray in 1c) |
| `servers/seed` | the embedded seed template "VLESS XHTTP behind nginx" | 1a |
| `servers/templates` | drafts, publishing, versions, diff, zip and git import/export | 1a core, 1b the rest |
| `servers/endpoint` | endpoint types (`vless-xhttp-tls`): field checks, connection URI, display name | 1c |
| `servers/proxy` | embedded xray-core: the proxy test, `Dialer`, config validation | 1c |
| `servers/dns`, `servers/dns/cloudflare` (+ `cloudflaretest`) | the DNS driver and its fake | 1c |
| `servers/geoip` | the IP → country suggestion (moved to `internal/platform/geoip` in 2c) | 1c |
| `servers/remote` | everything done on a server over SSH: step kinds, checks, stats batch, every command string | 1d |
| `servers/serverstest` | a fake VPS on `sshxtest.Server` that answers `remote`'s commands, plus a harness wiring module + fakes | 1d |
| `servers/provision`, `servers/deploy`, `servers/health`, `servers/stats`, `servers/retire` | job types and their logic | 1d–1g |
| `servers/checkhost` (+ `checkhosttest`) | the check-host.net client and its fake | 1f |
| `servers/agent` | agent sessions and `/agent/v1` | 1h |
| `servers/pages` | handlers and templ files | 1b onward |

### Rules

- **Tables are written ahead.** 1a writes the module's whole first migration `internal/modules/servers/migrations/00001_servers.sql` from [1a.md#tables](./1a.md#tables) (every Phase 1 table), with `migrations_test.go` for its constraints, as Phase 0 did. Until the first production deploy of a Phase 1 build, a later sub-phase that needs a change edits `00001` in place (and 1a.md's **Tables** with it). After it, only new migrations.
- **Remote commands live in one place.** Every shell command sent to a server is built by a function in `servers/remote` (`remote.Cmd*`). `serverstest.VPS` answers the same functions' output, so a command changed in one place changes in the fake too. No other package writes a shell string.
- **No real network in tests**, as in Phase 0: Cloudflare, check-host.net, ipinfo, reference sites, test objects and git hosts are `httptest` servers; servers are `serverstest.VPS`; the proxy test runs against an in-process xray server (`proxy/proxytest`).
- **Secrets.** Sealed AADs: `server:<id>:gen:<key>` (generated values), `server:<id>:params` (secret parameters, one JSON blob), `server:<id>:endpoint:<key>` (endpoint credential and params, one JSON blob), `deployment:<id>:params` (a deployment's secret parameters), `deployment:<id>:file:<path>` (deployed file content), `setting:cloudflare.api_token` (through settings). A provisioning job's root password is a job secret named `root_password`. Every job that renders registers every generated value and secret parameter with `Logger.Redact` before its first remote call.
- **Resource key** `server:<id>` for every job that changes a server or reads it over SSH: provision, the deploy kinds, rotation, restore, container logs, and the self-check (enqueued with `SkipIfBusy`, so a round skips a server busy with a mutating job). Retirement takes it by waiting (1g). The **proxy test takes no resource key**: `SkipIfBusy` counts *queued* jobs too, so a round that queues a self-check and then a delayed proxy test on the same key would always skip the proxy test. Instead the proxy test job asks `jobs.System.Busy(ctx, "server:<id>")` when it starts and, when a job is running on the server, stores nothing and ends. External checks, the reference check, `servers.evaluate` and `servers.resume` take no resource key.
- **Current files.** A server's current files are those of its latest `succeeded` deployment with `uploaded = 1`. Plans, `upload-files` removals, **Roll back**, rotation's restore and the Stack tab all use that; never just "the latest deployment" (restart, images and reboot deployments upload nothing).
- **Cancellation.** Every step that waits (DNS wait, `wait-http`, `wait-ssh`, smoke-test and proxy-test retries, check-host polling, retirement's waits) selects on `r.Cancelling()` and returns at once, so a cancel never waits out a 10-minute timeout. Every job type that writes a deployment has an `OnCancelled` that marks the deployment `failed` (error `cancelled`) and records its failure event with `cancelled: true`; the event's `NotifyIf` skips cancelled ones.
- **Actor** for admin actions `admin`; jobs `job:<id>`; the seed `system`.
- **i18n prefixes** of the module: `servers.`, `templates.`, `locations.`, `health.`, `stats.`, `agent.`, plus `event.<type>`, `notify.<type>`, `job.servers.<what>` as the platform's tests require.
- **Subscriptions don't exist in Phase 1.** Where a servers page or dialog names subscriptions or links (retire, rotate, overview), it asks the optional port `servers.UsageReader` (declared in 1d, nil in Phase 1) and shows "—" without it. Phase 2 implements it.

### Platform changes in Phase 1

| Change | Sub-phase |
|---|---|
| `module.Migrated`: `AfterMigrate(ctx) error`, called by `App.Migrate` after every module's migrations | 1a |
| `web.Routes.Shell`: the layout data builder, so a module's page can render `ui.Layout` | 1a |
| `module.DashboardDeclarer`: `Dashboard(ctx) ([]ui.DashboardArea, error)`; areas sorted by `Order` | 1d |
| `ui` components: `Diff` (unified, side by side), `Code` (read-only, highlighted, line anchors), `QR` (SVG), `SecretField` reuse | 1b, 1d |
| `ui` charts: `Sparkline`, `TimeSeries`, `Bar` as server-rendered SVG (no JS library) | 1f |
| `jobs.System.RetryWithTx(ctx, tx, id, by, RetryOptions{Payload, Secrets})`: a retry inside the caller's transaction that patches the payload and adds secrets | 1d |
| `sshx.Hop.Password` (root's password, first login only) | 1d |
| `sshxtest.Server`: `HandleFunc(match, fn)`, per-user keys and passwords, absolute SFTP paths | 1d |
| `jobs.System.Busy(ctx, key) (bool, error)`: whether a job in state `running` holds the resource key (+ `BusyExcept`, `CancelQueued`) | 1f (done) |
| `module.IntegrationDeclarer` (a module's row on Settings → Integrations) and `web.Routes.SettingsPages` (the side menu for a module's own settings page) | 1c (done) |

`/jobs?subject=<type>:<id>` and `/activity?subject=…&actor=…` already exist (0c); the server page's Jobs and Activity tabs link to them.

### Phase 1 decisions taken with the user (2026-10-07)

- **Validators are pure Go; the image stays distroless.** `shell` uses `mvdan.cc/sh/v3/syntax`; `nginx` uses `github.com/nginxinc/nginx-go-crossplane` (parse with unknown directives as errors and context/argument checks) on the sandboxed copy. No bundled `nginx` or `bash`. ADR 0009 is amended in 1a.
- **Agent hand-off** is in Phase 1, as its last sub-phase (1h).
- **Template editor**: vendored CodeMirror 6 bundle, built by `make editor` in Docker and committed (1b).
- **Git import**: public HTTPS repositories only, no stored credentials.
- **Firewall**: ufw.
- **Recovery notification**: `server.health_changed` to `healthy` notifies only from `blocked`/`down`.
- **Retire failure** notifies (`server.retire_failed`, on).
- **Seed images stay `:latest`**; the seed publishes with those warnings.
- **Provisioning** makes one attempt; the admin retries.
- **Cert expiring / disk low** notify once per crossing, not daily.
- **Verdict gaps and flap counting** (from the contract review, same day; defaults the user can still change): an evaluation counts toward flap protection only when it is the first to see a new proxy-test result; proxy works but SSH is unreachable → `degraded`; proxy fails, the stack runs, and no node abroad connects → `down`; anything the rules still don't match → `unknown` ("not enough data"). Details in [1f](./1f.md#verdict-rules).
- **IP → country**: `https://ipinfo.io/{ip}/country` by default, a setting the admin can change or clear (`servers.ip_country_url`; empty turns the lookup off). The result only preselects the location; the admin can always pick another.

## Phase 2: Subscriptions

Decided once for every Phase 2 contract (2026-10-08). A contract may narrow these; it may not contradict them without writing the change here. Read the Phase 1 section above too: its rules on tests, secrets, cancellation and pages still hold.

### Layout

One module, `subscriptions` (`internal/modules/subscriptions`), enabled in `modules()` in `cmd/proxier/main.go` **after** `servers` from 2a. Tables are prefixed `subs_`; the migration version table is `subscriptions_goose_db_version`. Its packages:

| Package | What | From |
|---|---|---|
| `subscriptions` | the `module.Module`, every optional interface, `New(Ports)`, `Usage()`; wiring only | 2a |
| `subscriptions/conf` | the settings section and its keys (a leaf, so services and pages read the same names) | 2a |
| `subscriptions/migrations` | `00001_subscriptions.sql`: every Phase 2 table | 2a |
| `subscriptions/store` | every SQL statement of the module, `FieldErrors`, retention constants | 2a, grows |
| `subscriptions/output` | what a link serves: formats, hiding, headers, stub entries, `Build`. Pure: no database, no clock of its own | 2a |
| `subscriptions/subs` | subscriptions: create, settings, servers and their order, preview, delete, the reactions to servers' events, usage | 2a |
| `subscriptions/links` | links: create, every action, the token, what a link serves now (`Serve`); the expiry scan (2c); cut-off (2d) | 2b |
| `subscriptions/fetch` | the public `GET`/`HEAD /s/{token}`, recording fetches, app detection, networks | 2b |
| `subscriptions/alerts` | fetch counts per link, the shared-link scan, network countries | 2c |
| `subscriptions/pages` | handlers and templ files | 2a onward |
| `subscriptions/substest` | test harness: the module on a fake `EndpointCatalog` (and a fake `Rotator` from 2d); a builder for the real-servers harness | 2a |

### Rules

- **Tables are written ahead.** 2a writes `internal/modules/subscriptions/migrations/00001_subscriptions.sql` from [2a.md#tables](./2a.md#tables) (every Phase 2 table) with `migrations_test.go`. A later sub-phase that needs a change edits `00001` in place (and 2a.md's **Tables** with it) **only if no Phase 2 build has been deployed**; when that isn't certain, it adds `00002_….sql` instead. Never both.
- **Ports only.** The module talks to servers through `servers.EndpointCatalog` (active servers, their endpoints, health and "since"), `servers.Rotator` (2d) and the endpoint values of `servers/endpoint` (`endpoint.Endpoint`, `endpoint.URI`, `endpoint.MaskedURI`). Its non-test files import **no other package** of `internal/modules/servers` (2a's `TestImportsOnlyServersPorts` enforces it). Tests may also import `servers/serverstest` and `servers/remote` (for `VPS.Hold`). It never reads a `servers_` table; a server is known by its id plus the name copied when it was added (names never change and are never reused).
- **Absent ports hide features** (ADR 0002): a nil `Catalog` means every subscription has no server in service (links serve "⚠️ No servers yet") and **Add servers** is hidden; a nil `Rotator` hides **Cut off**.
- **Nothing is cached.** Every fetch and every preview asks the catalog. Health hiding is decided at that moment from the state and its "since".
- **Secrets.** A link's token is sealed with AAD `link:<id>:token` and found by `vault.Lookup(token)` only. It appears only on its link page (masked text, **Reveal**, **Copy URL**, **QR**). Never in an event, a notification, a log line, a job payload, a list, search or the dashboard. Proxier's request log masks the token part of `/s/`, `/r/`, `/f/` paths (2b).
- **Clock.** `subscriptions.Module.Now` (default `time.Now`) is the clock of every service of the module; services read it through a function set in `Init`, so a test sets `mod.Now` once. The public rate limiter has its own clock (`App.PublicLimit.Now`).
- **Actors.** Admin actions `admin`; reactions to other modules' events and events raised by a public fetch `system`; jobs `job:<id>`; jobs created by an event subscriber have `CreatedBy: "event:<id>"`.
- **Events** carry names, not ids, for other things (`subscription: "Family"`), and list fields as one comma-joined string (`added: "nl-1, de-1"`), like `template.changed{fields}`: Activity renders payload fields as text. Times in payloads are `db.Time` strings, `""` for none.
- **i18n prefixes** of the module: `subs.`, `links.`, `alerts.`, `cutoff.`, plus `event.<type>`, `notify.<type>` (+ `.body`), `job.subscriptions.<what>` (+ `.step.<name>`), `settings.subscriptions`, `settings.field.subscriptions.<name>` (+ `.help`), as the platform's tests require.
- **Tests.** Most use `substest.New(t)`: the full app (`sitetest`) with the module on a fake catalog, no job workers, the module's clock in the test's hands. The cross-module edge cases (activation, retirement, rotation, cut-off) use the real servers module through `substest.WithServers(t)` (`serverstest.NewHarness(t, serverstest.StubProxy(), serverstest.WithModules(…))`): no XHTTP traffic, so every package of the module runs under `-race`. A test file that uses `substest` is an external test package (`package subs_test`, `links_test`, …): `substest` imports the module root, which imports every service, so an internal test package would make an import cycle.
- **Pages** follow the servers module's patterns: `r.Shell` + `ui.Layout`, every button a `ui.Action`, confirmation dialogs through `ui.Confirm`, and an action that needs fields is a **page styled as a dialog** (1g's retire page), not a `<dialog>`. Secondary actions live in an **Actions** area (as on the server page), not a "More ▾" menu (not built in Phase 1). No inline `style` or `script` (CSP); shared classes go into the platform's `app.css`. The designs (`RP-Subscriptions`, `RP-Subscription`, `RP-Links`, `RP-Link-new`, `RP-Link`, `RP-Phone-link`) are references: their formats column, network names ("MTS") and nav counters are not in Phase 2.

### Platform and servers changes in Phase 2

| Change | Sub-phase |
|---|---|
| `i18n.Localizer.Date` ("1 Dec 2026" / "1 дек 2026") and `ShortDate` ("1 Dec" / "1 дек") | 2a |
| `proxier.js`: sortable lists (`[data-sortable]`, drag by a handle, posts the order) and `[data-cursor]` (the row the cursor lands on after an htmx swap) | 2a |
| servers: `ServerEndpoints.Flag`; `endpoint.URI`, `endpoint.MaskedURI` and `endpoint.Type.Mask`; `serverstest.WithModules` (options applied before the site is built); the rotate dialog says "no links" when usage is empty | 2a |
| `web.Limiter` on the public chain: 60 requests per minute per client IP for `/s/`, `/r/`, `/f/` (not `/agent/`, which limits per session); `App.PublicLimit` | 2b |
| `httpx.MaskPath`: the request log and the 5xx error log write `/s/•••` instead of the token | 2b |
| `servers/geoip` moves to `internal/platform/geoip` (both modules look up countries) | 2c (done) |
| servers: the `Rotator` port (`RotationRequest`), `ErrNotActive` / `ErrNothingToRotate` re-exported | 2d (done) |

### Phase 2 decisions taken with the user (2026-10-08)

Claude proposed each of these; the user answered them all on 2026-10-08.

- **Display names stay as the admin entered the location** ("🇳🇱 Netherlands 1"). A link's language changes only its stub entries, not server names. (The fetch doc said "in the link's language"; the design already shows English names in a Russian link.)
- **Cut-off rotates one server at a time**, as the docs say: an event subscriber starts the next rotation when the previous one ends (the rollout pattern of 1e). A failure doesn't stop the rest; **Retry** queues that server again.
- **Network countries, on by default**: each network seen in fetches is looked up once a month by its **first address** (`198.51.100.0`, never the client's own), through `subscriptions.network_country_url` (default `https://ipinfo.io/{ip}/country`, empty turns it off).
- **Copy URL is one tap**: the link page carries the URL in the Copy button (the page is `no-store`; on screen it stays masked until **Reveal**).
- **New links default to Russian** stub texts (`subscriptions.link_language`).
- **Tokens are 256-bit** (`vault.NewToken`, 43 characters), like every other token in Proxier.
- **A subscription can't be deleted while any link points to it, a deleted one inside its tombstone included.** **Move links to…** moves the live ones; the tombstones keep the subscription until they end. A tombstone therefore always has its subscription.
- **The tombstone period and the fetch-log retention are settings**: `subscriptions.tombstone` (default 30 days) and `subscriptions.fetch_retention` (default 90 days).
- **`LinkIssuer` is Phase 4's**: its consumer (router scripts) decides its shape, including whether it runs inside the caller's transaction.
- **Hide unhealthy** offers blocked, down, degraded and unknown (paused never hides).
- **Confirmations** for Cut off, Delete link and Delete subscription are consequence dialogs (strength 2), not type-the-name.
- **An unknown or ended token gets the platform's plain public `404`** ("Not Found", like any unknown public path), not an empty body.
- **The rate limit** (60 requests a minute per IP) covers `/s/`, `/r/`, `/f/`; `/agent/` keeps its per-session limits.
- **Names** of links and subscriptions are unique by exact match (letter case counts).
- **Reordering** works by drag, `J`/`K` and ↑/↓ buttons; actions that need fields are **pages styled as dialogs**; no design extras in Phase 2 (operator names, nav badges, "More ▾" menus).
- **The gateway's access log** is fixed by a drafted, uncommitted change in `sh-main` (2d).
- **Four sub-phases**, 2a–2d.

## Phase 3: Routing

Decided once for every Phase 3 contract (2026-10-08). A contract may narrow these; it may not contradict them without writing the change here. Read the Phase 1 and Phase 2 sections above too: their rules on tests, secrets, cancellation, ports and pages still hold.

### Layout

One module, `routing` (`internal/modules/routing`), enabled in `modules()` in `cmd/proxier/main.go` **after** `subscriptions` from 3a. Tables are prefixed `routing_`; the migration version table is `routing_goose_db_version`. Its packages:

| Package | What | From |
|---|---|---|
| `routing` | the `module.Module`, every optional interface, `New(Ports)`, `Guard()`; wiring only | 3a |
| `routing/conf` | the settings section and its keys (a leaf) | 3a |
| `routing/migrations` | `00001_routing.sql`: every Phase 3 table, and the default list "Main" | 3a |
| `routing/store` | every SQL statement of the module, `FieldErrors`, retention constants | 3a, grows |
| `routing/change` | `Change`, `Marker`, `None`: the seam through which a change marks targets for sync (a leaf) | 3a |
| `routing/domain` | pure: normalising what the admin types, the domain-name check, covering, IDN, the registrable domain | 3a |
| `routing/selector` | pure: the selector grammar, tags, portals | 3a |
| `routing/snapshot` | pure: a service's names (`Set`), its canonical text and hash, diffs | 3a |
| `routing/sources` | resolving a selector into a `Set`: v2fly (includes, attribute filters), iplist (portals, miss vs unreachable), URL lists; the one HTTP client; `sourcestest` (fake v2fly, GitHub, codeload, iplist portals and URL hosts) | 3a, 3c (catalog downloads) |
| `routing/services` | services: add, switch source, custom services and their editor, rename, remove; snapshots and history | 3a, grows |
| `routing/own` | pure: ownership within a list (ADR 0013), the server-hostname guard, "covered by" | 3b |
| `routing/lists` | routing lists: lists, their services in order, the effective view targets get, warnings, the guard port | 3b |
| `routing/refresh` | the daily round, Refresh now, safety checks, accept anyway / dismiss, the digest | 3c |
| `routing/catalog` | the catalog refresh, generations, search, the reverse lookup | 3c |
| `routing/shadowrocket` | pure rendering, configs and their base versions, the public `/r/{token}/{name}.conf`, the fetch log | 3d |
| `routing/mtvpn` | the import: mtvpn's file rules, the preview, the import job | 3d |
| `routing/routeros` | pure: every RouterOS command and script, the read-output parsers, the `/import` output scan, and `Parse`/`ParseScript`, their inverses | 3e |
| `routing/routerostest` | a fake RouterOS router (and jump host) on `sshxtest.Server` that answers what `routeros` builds | 3e |
| `routing/routers` | routers: add and test, the plan (pure), sync, preview, triggers, history (3e); drift, unmanaged tags, awaiting setup, pause, removal (3f) | 3e, 3f |
| `routing/discovery` | discovery runs, classification, the save actions; `Browser`; `discovery/cdp` (Chromium over CDP with chromedp); `discovery/socks` (the per-run SOCKS listener through a server) | 3g |
| `routing/pages` | handlers and templ files | 3a onward |
| `routing/routingtest` | test harness: the app with the module on fake servers ports and `sourcestest` upstreams; a builder with the real servers module | 3a |

### Rules

- **Tables are written ahead.** 3a writes `internal/modules/routing/migrations/00001_routing.sql` from [3a.md#tables](./3a.md#tables) (every Phase 3 table) with `migrations_test.go`. A later sub-phase that needs a change edits `00001` in place (and 3a.md's **Tables** with it) **only if no Phase 3 build has been deployed**; when that isn't certain, it adds `00002_….sql` instead. Never both.
- **Ports only.** The module's non-test files import from `internal/modules/servers` only the root package: `ServerHostnames` (the guard), `EndpointCatalog` (discovery's server picker), `ProxyDialer` (discovery through a server). `TestImportsOnlyServersPorts` enforces it, as in subscriptions. It never reads a `servers_` table; a server is known by its id and its name.
- **Absent ports hide features** (ADR 0002): a nil `Hostnames` means there is nothing to guard (every list is allowed); a nil `Catalog` or `Dialer` means discovery visits **Direct** only. Servers' `RoutingGuard` (3b) is nil without this module, and provisioning then checks nothing.
- **The sync seam.** Every change that can alter what a target gets (a list's services or order, an accepted snapshot, a custom save, a tag rename, a router's list) calls `change.Marker.Mark(ctx, tx, change.Change{…})` **inside its own transaction**. Until 3e the marker is `change.None`; 3e's `routers.Marker` turns it into a coalesced sync of every router following the affected lists, 30 s later. A Shadowrocket config needs no mark: it is rendered on every fetch.
- **Remote commands live in one place.** Every RouterOS command and script is built by a function in `routing/routeros`; `routerostest` answers exactly what `routeros.Parse` and `routeros.ParseScript` recognise. No other package writes RouterOS text (servers' rule for `remote`, Phase 1).
- **Outbound HTTP** goes through `sources.Fetcher`: one client, user agent `proxier/<version>`, `http`/`https` only, a body cap per call, every base URL in `sources.Endpoints` (so tests point them at `httptest` servers). Interactive calls made inside an admin request (add a service, preview, switch source, import preview, a base config from a URL) have a 45 s overall deadline and 15 s per request; jobs use 30 s per request. Nothing remote ever runs inside a transaction.
- **No real network in tests**, as before: v2fly, GitHub, codeload, the iplist portals and URL hosts are `sourcestest`; routers and jump hosts are `routerostest`; Chromium is a fake `discovery.Browser` (the real one runs only behind an environment variable and in the exit demo); servers' ports are fakes from `routingtest`, or the real servers module through `serverstest` with `StubProxy()`. No routing test pushes XHTTP traffic, so every package of the module runs under `-race`.
- **Snapshots are text.** A snapshot holds its suffix and exact names sorted and newline-joined, with a SHA-256 hash of that canonical form; never one row per name. Each service has exactly one **accepted** snapshot (a partial unique index); that is the only thing targets ever get.
- **Ownership is computed, never stored.** `own.Compute` runs over a list's services in order whenever a page, a sync, a preview or a fetch needs it. Nothing is cached (Phase 2's rule).
- **Secrets.** A Shadowrocket token is `vault.NewToken()`, sealed with AAD `shadowrocket:<id>:token` and found by `vault.Lookup`; it appears only on its config page (masked, **Reveal**, **Copy**, **QR**), never in an event, a notification, a log, a job payload, a list, search or the dashboard; `httpx.MaskPath` already masks `/r/`. `routing.github_token` is a secret setting. The text of an imported `mtvpn.yaml` is never stored anywhere: only `services`, `service_lists` and `shadowrocket_base` are read from it (3d).
- **Clock.** `routing.Module.Now` (default `time.Now`) is the clock of every service of the module, read through a function set in `Init`, so a test sets `mod.Now` once.
- **Actors.** Admin actions `admin`; a public fetch `system`; jobs `job:<id>`; jobs created by a schedule have `CreatedBy: "schedule:<name>"`, by a change the actor of the change.
- **Events** carry names, not ids, for other things (`list: "Main"`, `service: "anthropic"`), lists as one comma-joined string, times as `db.Time` strings, `""` for none (Phase 2's rule). Subjects: `service:<id>`, `routing_list:<id>`, `router:<id>`, `shadowrocket:<id>`, `discovery:<id>`, and `routing:refresh` / `routing:catalog` for the daily round and the catalog (named "Upstream refresh" → `/routing/services`, "Catalog" → `/routing/search`).
- **Resource keys.** `router:<id>` for every job that connects to a saved router (test, sync, preview, drift check, probe, removal): one at a time per router. `service:<id>` for one service's refresh. None for the daily round, the catalog, the import and discovery (their queues' concurrency is enough).
- **i18n prefixes** of the module: `routing.`, `services.`, `lists.`, `catalog.`, `refresh.`, `routers.`, `shadowrocket.`, `mtvpn.`, `discovery.`, plus `event.<type>`, `notify.<type>` (+ `.body`), `job.routing.<what>` (+ `.step.<name>`), `settings.routing`, `settings.field.routing.<name>` (+ `.help`), as the platform's tests require.
- **Tests.** Most use `routingtest.New(t)`: the full app (`sitetest`) with the module on fake servers ports and a `sourcestest` upstream, no job workers until `StartJobs`, the module's clock in the test's hands. Router tests add `routerostest` routers (`h.Router(…)`). Test files that use `routingtest` are external test packages (`package services_test`, …), as in Phase 2.
- **Pages** live under `/routing/…` and follow the Phase 2 patterns: `r.Shell` + `ui.Layout`, every button a `ui.Action`, confirmations through `ui.Confirm`, an action that needs fields is a **page styled as a dialog**, secondary actions in an **Actions** area (no "More ▾" menu), no inline `style` or `script` (CSP), shared classes in the platform's `app.css`, sortable lists through `proxier.js` (2a). The designs (`RP-Lists`, `RP-List`, `RP-Services`, `RP-Service`, `RP-Custom-service`, `RP-Search`, `RP-Import`, `RP-Shadowrocket`, `RP-Shadowrocket-config`, `RP-Routers`, `RP-Router`, `RP-Router-add`, `RP-Phone-router`, `RP-Discover`, the dashboard boards) are references. Built although the docs don't require them (the user's choice): the list page's **Undo** (3b), a rejected snapshot's "likely cause" (3c), the per-hop status table (3f). Not built in Phase 3: nav counters, network operator names in fetch logs, **Generate a router script…** (Phase 4).
- **Nav**: group `routing`: **Lists** `/routing/lists` (order 10, 3b), **Services** `/routing/services` (20, 3a), **Search** `/routing/search` (30, 3c), **Discover** `/routing/discover` (40, 3g), **Routers** `/routing/routers` (50, `g r`, 3e), **Shadowrocket** `/routing/shadowrocket` (60, 3d). Only Routers has a go-to key (the design's `RP-Keys`); the rest are one `/` away.

### Platform and servers changes in Phase 3

| Change | Sub-phase |
|---|---|
| servers: `ServerHostnames` becomes `Servers(ctx) ([]ServerHostname, error)` (id, name, hostnames, IP per non-retired server), so the guard names the server | 3b |
| servers: the `RoutingGuard` port (`Covering(ctx, hostnames)`), set by `srv.SetRouting(rt.Guard())`; the new-server form refuses hostnames a routing list covers, naming the list and the service | 3b |
| sshx: `Client.Put(ctx, path, data)`: a plain SFTP write (no temporary file, `chmod`, `fsync` or rename), for RouterOS's minimal SFTP server; `SSH.SetSubject(ctx, address, subject)`; `sshxtest` takes relative SFTP paths | 3e (done; also `sshx.HopError` around every hop failure, `SSH.KnownHost`, `sshxtest.Server.Refuse/RefuseUploads`) |
| sshx: `Target.JumpFirstContact`, so a probe can pin a router's key on first contact while its jump host must already be confirmed | 3f |
| servers: `ProxyDialer.Dial` returns `servers.DialFunc` (a func type in the root package) instead of `proxy.DialFunc`, so routing imports no other servers package | 3g (done) |
| `httpx.MaskPath`, the public limiter on `/r/`, `PROXIER_CHROMIUM_URL` | already built (2b, 0a) |

### Phase 3 decisions taken with the user (2026-10-08)

Claude proposed each of these; the user answered them all on 2026-10-08 (the first four before the contracts were written, the rest after). Where the user chose otherwise than proposed, the line says so.

- **Seven sub-phases**, 3a–3g, with the Shadowrocket config and the mtvpn import (3d) **before** router sync (3e, 3f).
- **Discovery's headless visit is built** (3g): a Chromium sidecar (`chromedp/headless-shell`) on blackberry, driven over CDP with `github.com/chromedp/chromedp`; tests use a fake browser, the real one runs in the exit demo.
- **Awaiting setup is built in Phase 3** (3f): the state, the probe every 10 minutes for 7 days and "never connected", behind an internal `routers.Register`. Phase 4 only wraps it as the `RouterRegistrar` port.
- **v2fly include filters are honoured**: `include:x @a` takes only x's entries tagged `@a`, `include:x @-a` drops them, as v2fly defines it (mtvpn included the whole list). Attributes on plain entries are still dropped from the names, and affiliations (`&list`) are ignored (the data had none on 2026-10-08).
- **Redundancy inside one upstream service is kept** (mtvpn compatibility, so the first sync of a router mtvpn manages changes as little as possible): a name in both forms becomes suffix only, nothing else. A **custom** service absorbs both exact and suffix rows under its own suffixes when saved (with a note). Ownership works across services (ADR 0013). The Shadowrocket output additionally drops any rule another of its rules covers, as mtvpn's `shadowrocket_rules` did.
- **An unpinned iplist selector is stored pinned** to the portal it was found on when that portal isn't the first (`iplist:youtube.com` found on main stays unpinned; found only on beta, it becomes `iplist:beta:<sel>`), so a later refresh can't silently switch portals.
- **Switch source** accepts the new source's snapshot at once, without the safety checks.
- **Adding a service resolves it inside the request** (45 s overall, 15 s per request), not in a background job.
- **A selector that resolves to no usable name can't be added.**
- **The guard** refuses every save that would put a covering name into a list (adding a service, saving a custom service, creating one from discovery, adoption or the import). An upstream snapshot that comes to contain such a name is still accepted, but the name is left out of what targets get and the list shows a warning.
- **The shrink check** rejects when a snapshot of at least 20 names loses more than 50 % of them (the process doc), not the design's 30 %. Both are settings.
- **The digest** is a new event, `routing.refresh_digest`, recorded after every daily round and notifying only with news: a service changed, a snapshot was held back, or a service started failing (a service failing for days counts as news on its first day only; `refresh_failing` still notifies at the third failure). `routing.snapshot_accepted` never notifies by itself; `routing.snapshot_rejected` notifies by itself only outside the round.
- **"Unchanged" is strict**: a tag is unchanged only when the router's DNS entries for it (name and `match-subdomain`, `type=FWD` to the forwarder) and its address-list names all equal the desired ones.
- **Blocks are packed into files** of at most 2 000 names (the user's choice; Claude proposed one tag per file). After a failed file the router is read again, and each of its tags that landed is recorded as applied.
- **A router sync failure notifies only when the job gives up** (the user's choice; Claude proposed the 2nd consecutive attempt, or at once for **Sync now**): every failed attempt is recorded with `consecutive`, only the final one (the last attempt, or an error no retry fixes) notifies, and `routing.router_recovered` notifies only after such a notified failure.
- **A sync re-reads the router and re-plans on every attempt and every resume**, so a retry never pushes a stale plan; a tag's applied state is recorded as soon as its file succeeded.
- **Upload** is SFTP with a plain write, no SCP fallback: mtvpn's `scp` on macOS speaks SFTP since OpenSSH 9.0, so the user's router already accepts it (open question "to verify" 5 is answered by the exit demo).
- **Proxier logs in as its own `proxier` user in a restricted group** (the user's choice; Claude proposed group `full`): `/user group add name=proxier policy=read,write,ftp,ssh`, to verify on the first real router; Test connection checks an SFTP upload, and the import scan recognises "not enough permissions".
- **A router can be saved untested** (the user's choice; Claude proposed saving only after a passing test): it then waits, getting no sync from changes, until its first passing Test connection, which records `router_connected` and queues the initial sync.
- **Routers and jump hosts are confirmed by the admin** (`ConfirmFirstContact`), except an awaiting-setup router's own key, which its probe pins on first contact: nobody can confirm it, and its jump host must already be pinned.
- **The first hop** defaults to the tailnet when the node is running, else direct.
- **Drift auto-repair and the drift interval** are global settings; with repair off, drift notifies once per new set of drifted tags; a drift check that can't reach the router counts no failure and notifies nothing.
- **Removing a router keeps its pinned host keys** (Settings → SSH can forget them).
- **Shadowrocket configs can be deleted** (`routing.shadowrocket_deleted`); the header line's time is the render time.
- **The Shadowrocket base config is edited in CodeMirror** (the user's choice; Claude proposed a plain text area): a second entry point of the editor bundle with a small Shadowrocket mode, the textarea underneath so the form works without JS.
- **The import** puts every service into one routing list chosen at the top (default "Main"), and reuses an existing service with the same tag as it is, even when its selector differs.
- **A dotless search query** that finds nothing suggests discovering `<query>.com`; a query with a dot is suggested as typed.
- **The catalog** takes v2fly's list names from the repository archive it downloads anyway (one GitHub API request for the commit, one archive download), not from the trees API, and indexes every iplist domain through the custom export `{group}|{site}|{data}` (verified against the live service on 2026-10-08). Each source's catalog is written as a new generation in batches and switched in one short transaction.
- **Discovery's screenshots** are files under `PROXIER_DATA_DIR/discovery/<run>/`, not database rows (backups stay small), deleted with their run after 30 days. Its CDN and tracker lists are built into the code.
- **The Chromium sidecar gets its own Docker network** (`discovery`, shared only with `proxier`): the `proxier` network's subnet is the trusted-proxy range.
- **Three design extras are built** (the user's choice; Claude proposed none, as in Phase 2): **Undo** after reordering a list (3b), a rejected snapshot's **likely cause** from the catalog with **Add <selector>…** (3c), and the **per-hop status** table of a connect failure (3f). Nav counters are not built.
