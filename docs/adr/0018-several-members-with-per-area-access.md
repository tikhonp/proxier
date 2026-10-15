# Several members share one Proxier, each with a level in each area

Proxier started with one admin, and the rule was enforced in the CLI, the auth service and the table. Now several **members** sign in to one Proxier. The first, made by `proxier manage create-admin`, has the Owner preset and adds the others on Settings → Members. Each member has their own password, sessions, authenticator app, passkeys, language and Telegram chat, and a **level** in each **area**. Things in the owned areas belong to a member, and each member sees and changes only what their level reaches. The flows and every rule are in [members](../processes/platform/members.md) and [notifications](../processes/platform/notifications.md).

## One site, not tenants

There is one site, and its servers are shared infrastructure that every member's links use. Tenants would need their own servers to mean anything. Every link on a server serves the same credential ([ADR 0004](./0004-one-shared-credential-per-endpoint.md)), so tenants sharing a server are not separated. A Proxier for each person would repeat what a deployment has one of: home as the vantage point, the tailnet node, the Cloudflare zones, the bot and the master key. The members are family and friends trusted with part of one setup, not customers.

## Areas and levels

The areas follow the modules, so each module checks only its own area: `servers`, `subscriptions`, `routing`, `routerscripts`, `activity` and `settings`. Each area has its own ladder, and a higher level includes the lower ones:

- `servers`: none, view, manage. At `view` a member sees the servers in service (name, country, health state) and can put them in their subscriptions; everything else, templates included, needs `manage`.
- `subscriptions`, `routing`, `routerscripts`: none, own, view_all, manage_all. `own` makes things and reads and changes only the member's own. `view_all` also reads everyone's, and still changes only the member's own. `manage_all` reads and changes everything in the area. What every member shares (scripts and their versions, v2fly, iplist and URL services, the catalog, the default routing list) changes only at `manage_all`.
- `activity`: none, own, all. At `own` the Activity and Jobs pages show what the member caused or owns.
- `settings`: none, manage. Settings and Members.

Three presets fill a grant: **Owner** (the top level everywhere), **Operator** (Owner without Settings) and **Viewer** (the reading level everywhere, no Settings). Every signed-in route names its area and level, and a test walks every route of the app to hold that. Nobody gives a level above their own or changes their own levels, and the last member with `settings: manage` can't be removed.

## Ownership: own, view_all and manage_all

Subscriptions, links, routing lists, routers, Shadowrocket configs, custom services, imports, discovery runs and generations each have an owner: the member who made them. A member's links and routers are for their own people and places. Their URLs are secret, so another member at `own` must not read them. `view_all` exists for a member who helps without owning, such as the Viewer preset.

Every query of owned things takes an `access.Scope`, and the scope is itself the SQL argument of `? IN (0, owner_id)`. A forgotten scope is a compile error, not a leak. A thing outside what the member reads answers 404, never 403, so IDs don't tell what exists. A thing the member reads but may not change shows without its controls, and a forged change answers 403. Choices offer only what the member may change, so router scripts make links and routers only from the member's own subscriptions and lists.

These choices follow from that:

- Names (subscriptions, links, routing lists, routers, Shadowrocket configs) and tags stay unique across Proxier. A clash says the name or tag is taken and names nobody.
- Custom services are private to their owner and to `routing: manage_all`; `view_all` doesn't see another member's, and a list shows them by tag only. A tag is a service's identity on routers, so tags stay unique across members, and a tag another member's private service holds is refused as taken.
- `view_all` reads another member's link and Shadowrocket URLs and downloads their generations, secrets included, but can't make a fetch URL for them or Generate again from them, since the secrets carry over.
- The default routing list is shared, and only `manage_all` moves it. The mtvpn import, and adopting an unmanaged tag as a catalog service, add shared services, so they need `manage_all`. Held-back snapshots show on the dashboard only to `manage_all`.
- A service saved from a discovery run belongs to the run's owner.

## Cut-off and rotation stay with servers: manage

Cutting off a link rotates every server of its subscription, and each endpoint has one credential that every link on it shares ([ADR 0004](./0004-one-shared-credential-per-endpoint.md)). A cut-off by one member changes every member's links on those servers. So cut-off and rotation need `servers: manage`, whatever the member's level in `subscriptions`. A member without it can still disable their own link.

## Removal hands things to an heir

Removing a member ends their sessions and deletes their passkeys, authenticator app, levels and setup link. The row stays, so Activity still names them, and nothing they owned is deleted, so their links, Shadowrocket configs and fetch URLs keep working. Each module that owns things reacts to `member.removed` and gives the removed member's things to the **heir**, in its own transaction, recording `*.handed_over` when it moved something. The heir is the member who removed them; for a member who removed themselves, it is the first other member with `settings: manage`. A handover per module keeps modules out of each other's tables ([ADR 0002](./0002-modules-talk-through-ports-and-events.md)). Bringing a removed member back means adding them again.

## Notifications per member

One bot serves everyone, and each member has their own chat, toggles and language. Each event type has an **audience**. Most types go to the owner of the event's subject and to whoever manages its area: `manage_all`, or for platform and servers events `settings: manage` or `servers: manage`. A member's own sign-ins and ways in go only to that member, and changes to members go only to settings managers. A member at `own` or `view_all` hears only about their own things.

## Considered options

- **One Proxier for each person**: no shared state at all, but the servers, checks, tailnet node and bot are duplicated, and a server shared between two of them has one credential in both.
- **Tenants**: separate everything, servers included. That is more than family and friends sharing a setup need, and it brings per-tenant servers.
- **Roles with fixed permissions** (admin, operator, viewer): simpler to show, but "runs their own links without seeing others'" needs ownership anyway. The presets give the same short names over the per-area levels.
- **Disabling a member besides removing**: one more state to check everywhere. Removal keeps the history, and adding again brings someone back.

## Consequences

- Every new query of owned things takes a scope, and every new signed-in route names an area. Tests fail on a route without one.
- A level that reads everyone's things (`view_all`) reads their secret URLs too. Give it only to people who could be given those links.
- What ADR 0006 and ADR 0017 say about one person signing in now holds for each member.
- Several members on one real bot is still to be confirmed on the deployment ([open questions](../open-questions.md)).
