# Roadmap

The build follows the order you chose: **Servers → Subscriptions → Routing**, with the platform built underneath the first module and router scripts last. Each phase ends with a demo scenario that must work end to end on the real deployment.

Before phase 0 (done 2026-10-07): the design is final, in [ui/design/](./ui/design/README.md), on the [design canvas](https://claude.ai/artifact/TM7E3qaH69dDpV2aY4axec) (Rosé Pine page). The UI is templ + htmx ([ADR 0014](./adr/0014-server-rendered-ui-templ-htmx.md)).

## Phase 0: Platform skeleton

- Repository, CI, image, compose and gateway entries ([deployment](./deployment.md)).
- SQLite, per-module migrations, the vault (master key), settings.
- Admin creation, sign-in, sessions, lockout, password change ([sign-in](./processes/platform/sign-in.md)).
- Jobs: queues, steps, resource and coalescing keys, logs with redaction, cancel/retry, resume after restart. The scheduler ([jobs](./processes/platform/jobs.md)).
- Events, the dispatcher, the Activity page.
- Telegram channel and notification rules ([notifications](./processes/platform/notifications.md)).
- SSH client with jump hosts and pinned host keys. The tailnet node.
- UI shell from the [design](./ui/design/README.md) in templ + htmx: `tokens.css`, layout (header, sidebar, key line), the shared components, the keymap with the search pop-up and `?`, EN/RU switch.

**Exit:** sign in on `proxier.tikhonnnnn.com`. A demo job writes a live log, survives a container restart, and its failure reaches Telegram.

## Phase 1: Servers

- Locations, templates with drafts, versions, diffs, validators, zip and git import/export. The seed template converted from `servers-templates/proxy` ([template authoring](./processes/servers/template-authoring.md)).
- Cloudflare DNS driver.
- Provisioning with base bootstrap, DNS wait, stack, self-check, smoke test ([provisioning](./processes/servers/server-provisioning.md)).
- Redeploy, upgrade (single and rolling), restart stack, update images, container logs ([redeploy](./processes/servers/server-redeploy.md)).
- Rotation ([credential rotation](./processes/servers/credential-rotation.md)).
- Health: self-check, proxy test with the embedded xray client, check-host external checks, reference checks, verdicts, pause ([health](./processes/servers/server-health.md)).
- Stats ([stats](./processes/servers/server-stats.md)).
- Retirement ([retirement](./processes/servers/server-retirement.md)).

**Status (2026-10-08):** built, sub-phases 1a–1g. The exit demo below needs a real VPS and the real deployment; it is the user's to run ([1g](./build/1g.md#phase-1-exit-demo)). Left over: nothing has run on a real server or in a real browser yet.

**Exit:** a fresh VPS becomes `nl-2`, active and healthy, from the UI alone. Firewalling 443 on it from home produces a `blocked` verdict and a Telegram message, and removing the rule recovers it. Rotation changes the connection URI and the old one stops working.

## Phase 2: Subscriptions

- Subscriptions with ordered servers, formats, update interval, hide-unhealthy, automatic adding of new servers ([subscription management](./processes/subscriptions/subscription-management.md)).
- Links: create, copy/QR, disable, regenerate token, expiry, delete with tombstone, cut-off ([link lifecycle](./processes/subscriptions/link-lifecycle.md)).
- The public `/s/{token}` endpoint with headers and stub entries ([fetch](./processes/subscriptions/subscription-fetch.md)).
- Fetch log and shared-link alerts ([shared-link alerts](./processes/subscriptions/shared-link-alerts.md)).

**Status (2026-10-08):** built in four sub-phases, 2a–2d (contracts and **As built** notes in [build/](./build/README.md#phase-2-subscriptions)). Left for the user: the exit demo below with real phones and apps on `proxier.tikhonnnnn.com` (steps in [2d](./build/2d.md#phase-2-exit-demo)), the answers to open questions "to verify" 1 and 2 from it, and committing and deploying the gateway's access-log change in sh-main.

**Exit:** your phone and one family member use links from Proxier. Disabling a link turns that app's list into the stub entry on refresh. A link opened from many networks raises an alert.

## Phase 3: Routing

- Sources (v2fly, iplist, URL, custom), the catalog and search ([catalog search](./processes/routing/catalog-search.md)).
- Services and custom services ([service management](./processes/routing/service-management.md)).
- Routing lists with domain ownership and the server-hostname guard ([routing lists](./processes/routing/routing-lists.md)).
- Daily upstream refresh with safety checks and digest ([upstream refresh](./processes/routing/upstream-refresh.md)).
- Router sync over the tailnet with plan preview, drift repair and unmanaged tags ([router sync](./processes/routing/router-sync.md)).
- The hosted Shadowrocket config ([Shadowrocket](./processes/routing/shadowrocket-config.md)).
- mtvpn import ([mtvpn import](./processes/routing/mtvpn-import.md)).
- Discovery: catalog lookup, then the headless visit ([discovery](./processes/routing/domain-discovery.md)).

**Status (2026-10-08):** planned in seven sub-phases, 3a–3g (contracts and cross-cutting decisions in [build/](./build/README.md#phase-3-routing)); not built yet.

**Exit:** `mtvpn.yaml` is imported. The home router's first sync is a no-op for unchanged services. The phone subscribes to the hosted Shadowrocket config instead of copyparty. Typing a new site finds its domains, and the router gets them within a minute of saving.

## Phase 4: Router scripts

- Versions of `fresh-router.rsc` with parameter detection ([script versions](./processes/router-scripts/script-versions.md)).
- Generation with a router link and router registration, download, fetch URL ([script generation](./processes/router-scripts/script-generation.md)).

**Exit:** a factory-reset router fetches its generation with `/tool fetch`, imports it, appears in Routing, and is synced without any manual step except installing Proxier's key.

## Later

Out of the first version, in rough order of value. Each needs only an extension point or a new flow; nothing in the first version blocks them.

- Two-factor sign-in (TOTP).
- More formats: mihomo/Clash YAML (also for the router's mihomo container), sing-box JSON, a browser page with QR codes and app buttons.
- Replace a blocked server, keeping its name and credentials, with DNS repointed.
- Adopt servers built without Proxier.
- Probe agents for mobile networks and other ISPs.
- A VLESS Reality endpoint type and template, as a fallback when a domain gets blocked.
- Wildcard certificates issued centrally through Cloudflare DNS-01, optionally with proxy hostnames on cover domains, so server names stay out of certificate-transparency logs ([servers module](./modules/servers.md#dns)).
- Per-link (or per-subscription) credentials, with traffic statistics and quotas ([ADR 0004](./adr/0004-one-shared-credential-per-endpoint.md)).
- VPS billing and renewal reminders.
- IP ranges in services (Telegram, voice calls). Routing targets: plain list URL, mihomo rule-provider, sing-box rule-set.
- Discovery by HAR import and subdomain search.
- Telegram bot commands (status, pause checks, create a link).
- Notification channels: ntfy, email, webhook.
