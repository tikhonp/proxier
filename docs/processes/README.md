# Processes

Every flow of Proxier, step by step. Each document has the same shape:

- **Actors**: who and what takes part.
- A short introduction: what the flow is for.
- **Steps**: numbered. `→ event.name{fields}` marks the event a step records ([events](../events.md)).
- **Rules**: what always holds, whatever the path.
- **Edge cases (each is a test)**: every line has a test.

Read a flow's document before changing it. When the behaviour changes, the document changes in the same commit, and a new edge case comes with its test.

## Platform

- [Sign-in](./platform/sign-in.md): the first member, signing in and out, lockout, sessions, password change, passkeys, the authenticator app.
- [Members](./platform/members.md): areas and levels, adding a member with a setup link, changing levels, removing.
- [Jobs](./platform/jobs.md): queueing, running, steps, retries, cancellation, resume after restart.
- [Notifications](./platform/notifications.md): Telegram setup, rules, delivery.
- [Telegram bot](./platform/telegram-bot.md): commands in a member's own chat, conversations, polling.

## Servers

- [Template authoring](./servers/template-authoring.md): drafts, validation, publishing, versions, import and export.
- [Server provisioning](./servers/server-provisioning.md): from IP and root password to an active server.
- [Server redeploy](./servers/server-redeploy.md): redeploy, upgrade (single and rolling), parameter changes, restart, update images.
- [Credential rotation](./servers/credential-rotation.md): replacing a server's credentials, alone or as part of a cut-off.
- [Server health](./servers/server-health.md): checks, vantage points, verdicts, flap protection, pause.
- [Server stats](./servers/server-stats.md): metrics collection and display.
- [Server retirement](./servers/server-retirement.md): taking a server out of service.

## Subscriptions

- [Subscription management](./subscriptions/subscription-management.md): subscriptions, their servers and settings.
- [Link lifecycle](./subscriptions/link-lifecycle.md): creating, sharing, disabling, regenerating, expiring, cutting off, deleting.
- [Subscription fetch](./subscriptions/subscription-fetch.md): what the public link endpoint serves.
- [Shared-link alerts](./subscriptions/shared-link-alerts.md): detecting links passed on.

## Routing

- [Catalog search](./routing/catalog-search.md): the upstream catalog, its refresh and search.
- [Service management](./routing/service-management.md): upstream and custom services.
- [Domain discovery](./routing/domain-discovery.md): finding a website's domains.
- [Upstream refresh](./routing/upstream-refresh.md): the daily refresh and its safety checks.
- [Routing lists](./routing/routing-lists.md): lists, ownership, the server-hostname guard.
- [Router sync](./routing/router-sync.md): adding routers, syncing, drift, unmanaged tags.
- [Shadowrocket config](./routing/shadowrocket-config.md): the hosted config.
- [mtvpn import](./routing/mtvpn-import.md): moving an mtvpn setup in.

## Router scripts

- [Script versions](./router-scripts/script-versions.md): drafts, parameters, publishing.
- [Script generation](./router-scripts/script-generation.md): filling in a script for a router, download and fetch URLs.
