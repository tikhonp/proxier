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
| 1e Redeploy, upgrade, rollout, rotation | [1e.md](./1e.md) | planned |
| 1f Health and stats | [1f.md](./1f.md) | planned |
| 1g Retirement, server list, Phase 1 exit | [1g.md](./1g.md) | planned |
| 1h Agent hand-off | [1h.md](./1h.md) | planned |

The order is a dependency order: each builds only on finished ones. 1h is not needed for the Phase 1 exit demo (1g), so it may come after it.

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
| `servers/geoip` | the IP → country suggestion | 1c |
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
| `jobs.System.Busy(ctx, key) (bool, error)`: whether a job in state `running` holds the resource key | 1f |
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
