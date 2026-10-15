# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Proxier** is a self-hosted control panel for one person's proxy setup, shared with the members they add (a level per area, their own things, their own notifications; ADR 0018): it builds VPS proxy servers from templates and watches whether they work from Russia, gives people and devices subscription links, keeps domain routing on MikroTik routers and the Shadowrocket config in sync, and hosts the router setup script. One Go binary, one SQLite file, templ + htmx UI, deployed on blackberry (home) behind the sh-main gateway. Docs, code and logs are English; the UI is English and Russian.

**State:** every module (platform, servers, subscriptions, routing, router scripts) is built and deployed. New work comes from the backlog in `docs/overview.md`. Facts only the real deployment shows (`docs/open-questions.md`) need hardware, accounts or phones and are the user's: say so instead of faking such checks.

## Read first

- `docs/README.md` — the docs map and reading order. For code: `docs/architecture.md` → **`docs/development.md`** (layout, module interfaces, rules, test harnesses, real-browser checks) → `docs/adr/` → `docs/data-model.md` → the module doc → **the process doc of a flow before touching it**.
- [Glossary](#glossary) below. Use its words, never the ones under _Avoid_.
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
make editor          # rebuild the template editor's CodeMirror bundle in Docker (web/editor → internal/platform/ui/static/js/editor.bundle.js, committed)
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

## Glossary

The vocabulary is fixed: use these words in the UI, code, database and conversation, not the ones under _Avoid_.

### Servers

**Server**:
A VPS that Proxier built and manages. It is identified by its server name, never by its IP address.
_Avoid_: host, node, VPS, box, machine

**Country**:
Where a server is: an ISO 3166-1 alpha-2 code (`NL`), chosen when the server is created. Its name and flag are derived from the code, never stored; its code in lower case is the first half of a new server's name. Servers once had a **location** (a code, a name and a country, managed on its own page); ADR 0015 replaced it. The word survives only in old Activity entries and in `.Server.Location`, kept for older templates.
_Avoid_: location, region, geo

**Server name**:
`<code>-<number>`, e.g. `nl-1`: the country's code in lower case (a server created when servers had locations keeps its location's code, `ams-1`). It belongs to one server for its whole life, and numbers are never reused.
_Avoid_: hostname, label, id

**Management hostname**:
The DNS name Proxier and members use to reach a server, e.g. `nl-1.hosts.tikhonnnnn.com`.
_Avoid_: domain (alone)

**Proxy hostname**:
The DNS name that clients connect to and the TLS certificate is issued for. It is the same as the management hostname.
_Avoid_: SNI domain, cover domain

**Template**:
A named definition of what gets installed on a server: files, steps, generated values, endpoints and checks.
_Avoid_: recipe, playbook, script, preset

**Template version**:
An immutable, published snapshot of a template. Every server is built from exactly one.
_Avoid_: revision, release

**Draft**:
The editable, unpublished next version of a template or a router script.

**Manifest**:
The part of a template version that declares its parameters, generated values, files, steps, endpoints and checks.

**Parameter**:
A value a member supplies when building a server from a template, e.g. the Let's Encrypt email.
_Avoid_: variable, input, option

**Generated value**:
A secret that Proxier creates once per server (a UUID, a secret path) and reuses on every redeploy until it is rotated.
_Avoid_: random value, secret (alone)

**Stack**:
What a template version puts on a server: the rendered files and the containers running from them.
_Avoid_: deployment (for the files), install

**Provisioning**:
Turning a fresh VPS (IP and root password) into an active server: base setup, DNS, stack, smoke test.
_Avoid_: setup, bootstrap, install

**Redeploy**:
Rendering a template version again for an existing server and applying the difference.
_Avoid_: update, sync, re-provision

**Endpoint**:
One place on a server that clients connect to, with a protocol, host, port and parameters.
_Avoid_: inbound, node, config

**Endpoint type**:
A protocol Proxier understands: it knows how to build connection URIs for it and how to test it. There is one: VLESS over XHTTP with TLS.
_Avoid_: protocol (alone), transport

**Credential**:
The secret a client presents to an endpoint (the VLESS UUID). There is one per endpoint, shared by every link.
_Avoid_: key, password, user id

**Rotation**:
Replacing a server's rotatable generated values (the credential and secret path), so that every old connection URI stops working.
_Avoid_: reset, regenerate, revoke

**Retirement**:
Taking a server out of service for good: its DNS records are removed and it leaves every subscription, but its history is kept.
_Avoid_: delete, decommission, archive

### Health

**Check**:
One probe of a server from one vantage point, and its result.
_Avoid_: test (alone), ping, probe

**Self-check**:
A check run on the server itself over SSH: containers, certificate, local answers, resources.

**Proxy test**:
A check that connects through an endpoint the way a real client does and downloads a payload.
_Avoid_: e2e test, speed test

**External check**:
A check run by a third-party network of nodes (check-host.net) from chosen countries.

**Reference check**:
A check of home's own internet access (a domestic site and a foreign site) that decides whether the other checks can be trusted.
_Avoid_: connectivity test, self-test

**Vantage point**:
Where a check runs from: home (Proxier itself) or an external node in a named country.
_Avoid_: probe, location

**Health state**:
The verdict on a server, drawn from its recent checks: healthy, degraded, blocked, down, unknown or paused.
_Avoid_: status, uptime

**Blocked**:
The health state of a server that works on its own and can be reached from abroad, but can't be used from Russia.
_Avoid_: banned, censored, down

**Lifecycle state**:
Where a server is in its life: provisioning, active, failed or retired.
_Avoid_: status, health

### Subscriptions

**Subscription**:
A named, ordered set of servers, with output settings, that links point to.
_Avoid_: plan, group, bundle, profile

**Link**:
A named secret URL given to one person or device. It serves the connection URIs of exactly one subscription.
_Avoid_: user, subscriber, client, account, key

**Token**:
The secret part of a link's URL. Regenerating it makes the old URL stop working.
_Avoid_: id, hash, password

**Connection URI**:
One `vless://…` line in a link's output, for one endpoint.
_Avoid_: share link, config, key, proxy link

**Format**:
How a link's output is written. There are two: a plain URI list and a base64 URI list.
_Avoid_: type, encoding

**Stub entry**:
The single placeholder connection URI that a disabled, expired or deleted link serves. Its name tells the person why.
_Avoid_: dummy, fake server

**Fetch**:
One request for a link's URL by a client app, recorded with time, IP and app.
_Avoid_: hit, download, refresh

**Expiry**:
The moment after which a link serves its stub entry.
_Avoid_: deadline, TTL

**Shared-link alert**:
A warning that a link is fetched from more networks or apps than one person's devices would explain.
_Avoid_: abuse alert, leak alert

**Cut-off**:
Disabling a link and rotating every server in its subscription, so the link's holder loses access even with copied URIs.
_Avoid_: ban, revoke, kill

### Routing

**Service**:
A named set of domains from one source, e.g. `v2fly:anthropic`. It is what gets added to routing lists and installed on routers.
_Avoid_: app, site, category, list

**Source**:
Where a service's domains come from: v2fly, iplist, a URL, or Proxier itself (custom).
_Avoid_: provider, origin, feed

**Selector**:
How a service is named in its source, written `source:selector`, e.g. `iplist:beta:cloudflare.com`.
_Avoid_: id, path, query

**Tag**:
A service's identity on a router: its selector without the source prefix. Exactly one service has each tag.
_Avoid_: comment, label, name

**Custom service**:
A service whose domains a member writes and edits in Proxier.
_Avoid_: own list, manual list, user list

**Snapshot**:
A service's parsed domains as fetched at one moment. The newest accepted snapshot is what targets get.
_Avoid_: cache, version

**Suffix domain**:
A domain that also covers all of its subdomains.
_Avoid_: wildcard, `domain:` (v2fly spelling)

**Exact domain**:
A domain that covers only itself.
_Avoid_: `full:` (v2fly spelling), host

**Catalog**:
The searchable index of every selector the upstream sources offer, and of the domains they contain.
_Avoid_: library, directory

**Discovery**:
Finding the domains a website uses, by catalog lookup and a headless-browser visit, so they can become a custom service.
_Avoid_: crawl, scan, resolve

**Routing list**:
A named set of services. Each target follows exactly one routing list.
_Avoid_: profile, policy, ruleset, service list

**Target**:
A place a routing list is applied to: a router or a Shadowrocket config.
_Avoid_: destination, sink, output

**Router**:
A MikroTik that Proxier keeps in sync with a routing list.
_Avoid_: device, gateway, mikrotik

**Jump host**:
The tailnet machine Proxier connects through to reach a router it can't reach directly.
_Avoid_: bastion, proxy

**Infra pin**:
A router entry with a comment starting `mtvpn:`. It belongs to the router script, and sync never touches it.
_Avoid_: system entry, protected entry

**Unmanaged tag**:
A service tag found on a router that Proxier never installed there.
_Avoid_: foreign entry, orphan

**Sync**:
Bringing a target in line with its routing list.
_Avoid_: push, deploy, apply

**Drift**:
A difference between what a router holds and what Proxier last installed on it.

**Shadowrocket config**:
A target that Proxier hosts: a base config with the routing list's rules spliced in, served at a secret URL.
_Avoid_: profile, conf

**Base config**:
The hand-written part of a Shadowrocket config, which Proxier copies verbatim.

### Router scripts

**Router script**:
A RouterOS script that sets up a factory-fresh router, kept in versions, e.g. `fresh-router.rsc`.
_Avoid_: rsc, template, bootstrap

**Script parameter**:
One literal `:local` value in a router script's PARAMETERS block, filled in for each router.
_Avoid_: variable, placeholder

**Computed value**:
A `:local` value of a router script's PARAMETERS block that is an expression, not a literal; shown, never filled in.
_Avoid_: derived parameter

**Generation**:
A router script version filled in with one router's parameter values.
_Avoid_: build, render, instance, copy

**Fetch URL**:
A short-lived, single-use secret URL that a router downloads one generation from.
_Avoid_: download link, one-time link

### Platform

**Member**:
A person who signs in to Proxier with their own username, password, sessions, authenticator app and passkeys, and a level in each area. `proxier manage create-admin` makes the first one, member 1, as Owner; the others are added on Settings → Members. A removed member keeps their row, so history names them. In code and history, a member acts as `member:<id>`; the old actor `admin` is member 1.
_Avoid_: user, account, admin

**Area**:
A part of Proxier a member has one level in: `servers`, `subscriptions`, `routing`, `routerscripts`, `activity`, `settings`. Areas follow the modules.
_Avoid_: section, permission, scope (that is whose things a query covers)

**Level**:
How far a member goes in one area, on that area's own ladder: `none`, `view`, `manage` (servers, settings); `none`, `own`, `view_all`, `manage_all` (subscriptions, routing, router scripts); `none`, `own`, `all` (activity). A member's levels in every area are their **grant**.
_Avoid_: role, permission, right

**Owner**, **Operator**, **Viewer**:
The presets that fill a grant: the top level everywhere; Owner without Settings; reading everything and no Settings.
_Avoid_: admin, superuser, role

**Owner** (of a thing):
The member a subscription, link, routing list, router, Shadowrocket config, custom service, import, discovery run or generation belongs to: who made it, or the heir of a removed member. Not the Owner preset. A member's **scope** in an area is whose things they read (their own at `own`, everyone's from `view_all`) and change (everyone's only at `manage_all`).
_Avoid_: author, creator (the actor of `*.created` stays who made it), tenant

**Heir**:
The member who takes over a removed member's things: whoever removed them, or for a member who removed themselves, the first member in with `settings: manage`.
_Avoid_: successor, inheritor

**Setup link**:
The single-use secret URL, valid 24 hours, where a newly added member chooses their password.
_Avoid_: invite, invitation, activation link

**Passkey**:
A WebAuthn credential on a member's device or in a password manager that signs in alone, with no username or password. Optional; any number of them.
_Avoid_: security key (that is one kind of device that holds one), WebAuthn credential (in the UI), FIDO key

**Authenticator app**:
The optional TOTP second step of a password sign-in: a 6-digit **code** from an app on the phone. In code and events, `totp`.
_Avoid_: 2FA app, OTP, one-time password, MFA

**Code step**:
The page after a right password while the authenticator app is on: the sign-in waits there (5 minutes, 5 tries) for the code.
_Avoid_: second factor page, challenge

**Job**:
A unit of background work (provisioning, a sync, a check round) with a queue, a state, steps and a live log.
_Avoid_: task, run, process

**Event**:
An append-only record that something happened. Events feed the activity history and notifications.
_Avoid_: log entry, audit record

**Notification**:
A Telegram message sent to a member, in their chat and language, because of an event that concerns them (their own thing or sign-in, an area they manage, or a change to members for who manages Settings).
_Avoid_: alert (reserved for shared-link alerts), message
