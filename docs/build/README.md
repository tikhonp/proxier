# Build contracts

Phase 0 ([roadmap](../roadmap.md#phase-0-platform-skeleton)) is built in sub-phases, one per fresh context. Each sub-phase has a **contract** here, written before it starts:

| Sub-phase | Contract | State |
|---|---|---|
| 0a Skeleton | (this file, [Built in 0a](#built-in-0a)) | done 2026-10-07 |
| 0b Sign-in + UI shell | [0b.md](./0b.md) | done 2026-10-07 |
| 0c Jobs + events dispatch | [0c.md](./0c.md) | done 2026-10-07 |
| 0d Telegram notifications | [0d.md](./0d.md) | done 2026-10-07 |
| 0e SSH, tailnet, backups, deploy | [0e.md](./0e.md) | done 2026-10-07 (the exit demo on the real deployment is the user's) |

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
