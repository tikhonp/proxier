# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Proxier** is a self-hosted control panel for one person's proxy setup, shared with the members they add (a level per area, their own things, their own notifications; ADR 0018): it builds VPS proxy servers from templates and watches whether they work from Russia, gives people and devices subscription links, keeps domain routing on MikroTik routers and the Shadowrocket config in sync, and hosts the router setup script. One Go binary, one SQLite file, templ + htmx UI, deployed on blackberry (home) behind the sh-main gateway. Docs, code and logs are English; the UI is English and Russian.

**State:** every module (platform, servers, subscriptions, routing, router scripts) is built and deployed. New work comes from the backlog in `docs/overview.md`. Facts only the real deployment shows (`docs/open-questions.md`) need hardware, accounts or phones and are the user's: say so instead of faking such checks.

## Read first

- `docs/README.md` — the docs map and reading order. For code: `docs/architecture.md` → **`docs/development.md`** (layout, module interfaces, rules, test harnesses, real-browser checks) → `docs/adr/` → `docs/data-model.md` → the module doc → **the process doc of a flow before touching it**.
- `CONTEXT.md` — the glossary. Use its words, never the ones under _Avoid_.
- `docs/overview.md` (scope and the backlog), `docs/open-questions.md` (what the real deployment hasn't confirmed yet).
- Docs describe Proxier as it is now: no phases, build history or dated decision notes (git has those).
- UI: `docs/ui/README.md` (what each screen does) and `docs/ui/design/README.md` + `tokens.css` + `screens/RP-*.dc.html` (how it looks: the Rosé Pine page of the design canvas).

## Rules for all code

- **Every edge case listed in a process doc gets a test.** Behaviour changes update the docs in the same commit.
- **Modules** (ADR 0002) are packages under `internal/modules/<name>`, enabled in `modules()` in `cmd/proxier/main.go`. A module never reads another module's tables; there are no foreign keys across modules; it talks to others through ports (small interfaces) and events. Tables are prefixed by module (`servers_`, `subs_`, `routing_`, `rscripts_`); the platform's tables have no prefix.
- **Every state change records its event in the same transaction** (`events.Catalog.Record(ctx, tx, …)`), with an actor (`member:<id>`, from `web.Actor(ctx)` in a handler; `system`, `cli`, `job:<id>`, `agent:<id>`). Old rows say `admin`, which is member 1. Payloads never hold secrets. A change that changes nothing records nothing.
- **Nothing slow or remote inside a transaction.** `db.DB.Write` holds the only write connection of the process (never touch `d.W` inside its callback). Read, act remotely, then write. Notifications and other reactions are queued after the commit.
- **Access** (ADR 0018, `docs/processes/platform/members.md`): every signed-in route registers on its area (`r.Area(access.X)`), and every query of owned things takes an `access.Scope` (`web.Scope(ctx, area)` in handlers, `access.All` in jobs and reactions). Another member's thing outside the scope is a 404; one read but not changeable is a 403.
- **Secrets only through the vault**: sealed with an AAD naming where they live (`"setting:<key>"`, `"job:<id>:payload"`), looked up by `vault.Lookup` (HMAC), never compared or stored in plain text, never logged.
- **Schema changes only in new migrations**; every module's schema is deployed.
- Stored times are UTC `db.Time` (fixed-width text); shown in the display time zone.
- Comments say why, not what. Match the surrounding code.
- No error reporting service: errors are only logged (Sentry was removed on purpose; don't add it back unasked).

## Commands

```sh
make test            # go test -race, except servers/proxy, provision, deploy (xray's XHTTP client has its own data race; they run without -race) — SQLite in temp files, no services needed
make check           # templ generate + gofmt + vet + generated-file diff + tests (CI minus lint/vuln); stage regenerated *_templ.go first
make lint            # golangci-lint in Docker (not installed on the host; default linters); Docker Desktop must run (open -a Docker)
make vuln            # govulncheck
make dev             # docker compose: air hot reload on :8080; needs .env (cp .env.example .env, fill the key)
make manage ARGS="migrate status"     # inside the dev container
make editor          # rebuild the template editor's CodeMirror bundle in Docker (web/editor → ui/static/js/editor.bundle.js, committed)
make check-editor    # make editor + fail if the committed bundle differs (part of make check; needs Docker running)
make build-image     # code.tikhonnnnn.com/tikhon/proxier:local
go run ./cmd/proxier serve            # on the host, with PROXIER_MASTER_KEY, PROXIER_BASE_URL, PROXIER_DATA_DIR=./data
```

`proxier manage migrate up | down <module> | status | create-admin | reset-password <username> | reset-2fa <username>`, `proxier version`, `proxier healthcheck`. Migrations also run on every `serve` start; a failing one keeps the previous schema and exits.

New migration: add `NNNNN_name.sql` (goose, `-- +goose Up` / `-- +goose Down`) to the module's `migrations/` directory, embedded through the module's `Migrations() fs.FS`. Each module has its own version table `<module>_goose_db_version`. Prefer `STRICT` tables.

templ: after editing `*.templ` run `make templ`; generated `*_templ.go` files are committed (CI fails on a stale one). The templ version in `Makefile`, `Dockerfile` and CI must match `github.com/a-h/templ` in `go.mod`.

Before committing: `make check`, `make lint`, `docker build .`. No Co-Authored-By in commits.

## Layout

```
cmd/proxier/                  serve | manage <command> | version | healthcheck; modules() lists the enabled modules
internal/platform/            App: Open(cfg, log, modules...) → Migrate → Serve; the platform is itself module "platform"
  config obs db vault events settings migrations httpx      the base
  access auth web ui pages i18n sitetest                    areas and levels, sign-in and members, HTTP, templ shell, EN/RU
  jobs notify sshx tailnet backup geoip                     jobs + scheduler, Telegram, SSH, tsnet, backups
  agentapi                    the agent hand-off's shared token, limits and form
  module/                     Module and its optional interfaces (docs/development.md#module-interfaces)
internal/modules/servers      templates, provisioning, deploys, rotation, health, stats, retirement, /agent/v1
internal/modules/subscriptions  subscriptions, links, public /s/, alerts, cut-off
internal/modules/routing      services, lists, refresh, catalog, Shadowrocket /r/, mtvpn import, router sync, discovery
internal/modules/routerscripts  script versions, generations, public /f/, /agent/v1/router-script
web/editor/                   source of the CodeMirror bundle (package.json, editor.js, build.sh)
```

## Deployment

Image `code.tikhonnnnn.com/tikhon/proxier` (Forgejo's registry; distroless static, uid 65532, `/data` volume owned by it, `ENTRYPOINT /bin/proxier`, `CMD serve`), built by Forgejo Actions on code.tikhonnnnn.com (`.forgejo/workflows/ci.yaml`, secret `REGISTRY_TOKEN`) only after tests pass: main → `:<sha>`, `:main`, `:latest`; tag `v*` → `:<tag>`; `APP_VERSION=<ref>-<sha7>`. Production compose lives in `~/projects/sh-blackberry/proxier.yaml` and the gateway in `~/projects/sh-main` (tunnel port 10005); the user commits those. Details in `docs/deployment.md`.
