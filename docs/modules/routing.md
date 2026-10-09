# Routing

The routing module decides which domains go through the VPN, and keeps every place that needs that list in sync: MikroTik routers (over SSH) and the Shadowrocket config on the phone (hosted by Proxier). It is `mtvpn.py`, always on, driven from the UI, and it replaces the copyparty uploads and the hosted service-list files.

## From mtvpn to Proxier

| mtvpn | Proxier |
|---|---|
| `services:` and `service_lists:` in `mtvpn.yaml`, plus hosted selector lists | **Routing lists** in the UI. Several, one default ([routing lists](../processes/routing/routing-lists.md)). |
| Selectors `iplist:…`, `v2fly:…`, URLs, `<tag>=<url>`, bare names | The same selectors, the same tag rules ([domain sources](../integrations/domain-sources.md)). |
| Your own domain lists as files on copyparty | **Custom services**, edited in Proxier. |
| `search`, `domains` | The catalog search page and the service preview ([catalog search](../processes/routing/catalog-search.md)). |
| `add`, `update`, `remove`, `--prune` | Automatic **sync** whenever a list, a service or a snapshot changes. Proxier removes tags it installed when they leave the list. Tags it never installed are reported, never deleted. |
| `update` run by hand | A **daily refresh** with safety checks and a digest ([upstream refresh](../processes/routing/upstream-refresh.md)). |
| `list -v` | The router page: what is installed, by tag, compared with what should be. **Drift** is detected and repaired. |
| `-n` dry run | **Preview sync**: the plan and the exact RouterOS script, before or instead of running. |
| `-r JUMPHOST:HOST` | A router's connection: host, user, optional jump host, over the tailnet. |
| `shadowrocket` + copyparty PUT | A **hosted Shadowrocket config** at a secret URL, always current ([Shadowrocket config](../processes/routing/shadowrocket-config.md)). |
| — | **Discovery**: type a website, get its domains ([discovery](../processes/routing/domain-discovery.md)). |
| — | History of every snapshot with diffs, notifications, a one-time [import of `mtvpn.yaml`](../processes/routing/mtvpn-import.md). |

Router-side behaviour stays **compatible with mtvpn**: same object types, same tags, same `mtvpn:` infra pins, same address-list and forwarder names. A router mtvpn manages today needs no migration ([ADR 0010](../adr/0010-router-contract-stays-mtvpn-compatible.md)).

## Sources and selectors

A **service** is a named set of domains from one **source**. Sources are pluggable. The first four:

| Source | Selector | Notes |
|---|---|---|
| `v2fly` | `v2fly:anthropic` | Lists from `v2fly/domain-list-community`, with `include:` followed recursively. `regexp:` and `keyword:` lines are skipped (RouterOS can't express them) and reported. |
| `iplist` | `iplist:youtube.com` (a site), `iplist:apple` (a group), `iplist:beta:cloudflare.com` (portal pinned) | Three near-disjoint portals tried in order: main, beta, russia. An unreachable portal is never treated as "not found". |
| `url` | `https://…`, or `mine=https://…` to name the tag | Any list in v2fly or plain format. |
| `custom` | — (made in Proxier) | Domains edited in the UI. |

A bare name (`anthropic`) means `v2fly:anthropic`, as in mtvpn. The UI always spells selectors with their prefix. Full grammar, tag rules and source quirks: [domain sources](../integrations/domain-sources.md).

## Services

- Every service has a **tag**, which is its identity on routers (`comment=`) and is unique across services. Tags are `[a-z0-9][a-z0-9._!-]{0,62}`, never empty and never `telegram-cidr`. It is the selector without the source prefix (`v2fly:anthropic` → `anthropic`). URL sources take the named tag, the iplist query value, or the file name without its extension. A custom service's tag is chosen at creation (a slug of its name) and can be renamed. A rename makes every router replace the old tag with the new one. A custom service also records its **origin**: made in the editor, by Discover, by the mtvpn import or adopted from a router.
- Selectors are stored with their prefix (`v2fly:anthropic`). An iplist selector found on a portal other than main is stored pinned to it (`iplist:beta:apple`).
- Adding a selector whose tag is already taken doesn't create a second service. Proxier offers to **switch the source** of the existing one instead, e.g. from `iplist:apple` to `iplist:beta:apple`, or from `v2fly:youtube` to `youtube=https://…/my-youtube.txt`. Naming a URL with another tag makes a new service.
- A service's domains are a **snapshot**: suffix domains (they cover subdomains) and exact domains, plus the entries that were skipped and why. The newest **accepted** snapshot is what targets get. Upstream services are refreshed daily. A custom service gets a new snapshot every time it is saved.
- The **service page** shows the selector, source and portal, tag, counts, last check, the domain list (filterable, suffix and exact marked), the skipped entries, the snapshot history with diffs, the routing lists it's in, and which of its domains another service in the same lists already owns.
- A service can be removed from Proxier only when no routing list contains it.

Flows: [service management](../processes/routing/service-management.md).

## Catalog

The **catalog** indexes every selector the upstream sources offer: v2fly list names and iplist sites and groups per portal. It is refreshed daily and on demand. It also has a **reverse index** from domain to the selectors that contain it, used by discovery's catalog lookup ("claude.ai is in `v2fly:anthropic`"). Search and the index: [catalog search](../processes/routing/catalog-search.md).

## Discovery

Type a website. Proxier first looks it up in the catalog, suggesting existing services that contain it. Then it opens the site in headless Chromium, directly or through one of your servers, and records every hostname it loads. You tick which ones to keep, and they become a custom service (or extend one) in the routing lists you pick. Nothing changes until you save. Details: [discovery](../processes/routing/domain-discovery.md).

## Routing lists

- A **routing list** is a named, ordered set of services. "Main" exists from the first start and is the default for new targets. Each target follows exactly one list. A service can be in many lists.
- **Ownership**: within a list, every name is installed once. It is computed (package `own`) whenever a page, a sync or a fetch needs it, never stored.
  - Names that trip the guard, and a router's infra pins, are taken out first: installed by nobody, they cover nothing.
  - A name that another service's broader suffix domain covers is dropped.
  - A name that several services share in the same form goes to the first of them in list order that installs it.
  - Names under a service's own suffixes stay (mtvpn compatibility).

  A list's services show **owned / total**: the names the service installs there, and the names in its snapshot. The service page says, per list, why the others aren't installed ("owned by anthropic (first in Main)", "covered by anthropic.com (anthropic)", "left out: covers nl-1.hosts.tikhonnnnn.com (nl-1)"). Removing, editing or reordering services moves ownership, and every affected tag is re-synced ([ADR 0013](../adr/0013-one-owner-service-per-domain.md)).
- **Server hostname guard**: a list can't contain a domain that equals or covers a server's management or proxy hostname (e.g. a custom service with `tikhonnnnn.com` would cover `nl-1.hosts.tikhonnnnn.com`). Routing those names would send Proxier's own checks, and the router's mihomo connection, into the tunnel. Adding a service to a list and saving a custom service that is in a list are refused, naming the server and domain; a refresh that brings such a name is accepted, and the name is left out of what targets get, with a warning on the list. Provisioning asks routing (`RoutingGuard`) and refuses a hostname any listed name covers.

Flows: [routing lists](../processes/routing/routing-lists.md).

## Targets

Target kinds are pluggable. The first two:

**Router** (MikroTik, RouterOS 7):

| Field | Meaning | Default |
|---|---|---|
| Name | e.g. "Home", "Parents". | — |
| Routing list | The list it follows. | default list |
| Host, SSH port, user | How to reach it, e.g. its LAN address `10.230.1.1` behind a jump host. | port 22 |
| Jump host | Optional tailnet machine (host, port, user) to connect through. | none |
| Address list | RouterOS firewall address-list name. Must match the router script. | `to_vpn_list` |
| DoH forwarder | RouterOS DNS forwarder name. Must match the router script. | `vpn-doh` |
| State | `awaiting setup` (registered through `routers.Register`, never connected, no failure alerts, probed every 10 min for 7 days), `active`, `paused` (no syncs, previews or drift checks), or `removing` (a removal sync cleans what Proxier installed, then the router is deleted). | active, or awaiting setup when created by router scripts |

**Shadowrocket config**: a name, a routing list, a base config (versioned), a rule policy (default `PROXY`), and a secret URL `https://proxier.tikhonnnnn.com/r/{token}/{name}.conf`.

## Router sync

Sync makes a router hold exactly what its routing list says, per tag, using the RouterOS objects mtvpn uses ([RouterOS integration](../integrations/routeros.md)):

1. Read the router: every DNS static entry in the address list (comment, name, match-subdomain, type, forward-to), and every non-dynamic address-list entry, grouped by tag (the DNS entries' comments).
2. Compute the desired domains per tag from the routing list (after ownership), the router's infra pins left out.
3. Plan per tag, comparing desired against what Proxier last applied and what the router actually holds, strictly (name and match-subdomain, `type=FWD` to the forwarder, and the address-list names): **unchanged**, **record** (matches but was never applied: recorded, nothing pushed), **update** (one idempotent service block: remove the tag, adopt untagged duplicates, add everything), **remove** (a tag Proxier installed that left the list, or a tag owning nothing in it), **forget** (applied but gone from the router).
4. Tags on the router that Proxier never installed are **unmanaged**: kept per router with their names, notified once per new set and never deleted on their own: the admin can **Adopt** them (the catalog's selector, an existing service, or a custom service made from the router's names), **Remove** them from the router, or **Ignore** them.
5. Push the blocks in the order that never loses a name (updates gaining names, other updates, removals), packed into files of at most 2 000 names over SFTP and `/import`; scan the output, delete each file, verify, and record each tag as soon as its file succeeded.

Routers are added through **Test connection** (the jump host's and the router's fingerprints confirmed by the admin) and **Save**, which may also be done untested: such a router waits for its first passing test before any sync. The first hop is direct or through the tailnet node (the default while it runs). Syncs are triggered by changes (30 s delay, coalesced per router), by **Sync now**, by **Resume**, and by repair after a **drift check** (every `routing.drift_every`, read-only, against what Proxier applied; with `routing.drift_repair` off it notifies and offers **Repair**). A router can be **paused** and **removed**, keeping or cleaning what Proxier installed. Every attempt re-reads the router and re-plans; a failure is recorded per attempt, retried after 5 min, 15 min and 1 h, and notifies only when the job gives up. Entries commented `mtvpn:…` (infra pins) and dynamic entries are never touched. Details: [router sync](../processes/routing/router-sync.md).

## Shadowrocket config

Proxier renders the config when it's requested: the base config verbatim, with the routing list's rules spliced into `[Rule]` before `FINAL` (or ending with `FINAL,DIRECT`). Rules are `DOMAIN-SUFFIX` / `DOMAIN` with the rule policy, one `# <tag>` block per service, after ownership and suffix coverage. The phone subscribes to the URL once, and from then on every change reaches it on its next refresh. Details: [Shadowrocket config](../processes/routing/shadowrocket-config.md), [Shadowrocket integration](../integrations/shadowrocket.md).

## Pages

- **Routing lists**: each list with its service count, domain count and targets. The list page holds the ordered services (drag to reorder, add from search or services), the domain total, its targets with their sync state, and warnings (guard refusals, rejected snapshots).
- **Services**: every service, with source, tag, domain counts, last refresh, state (ok / rejected snapshot waiting / failing), and the lists it's in. **Add service** opens search or a selector field. **New custom service** opens the domain editor.
- **Search**: the catalog search ([catalog search](../processes/routing/catalog-search.md)).
- **Discover**: the discovery form and its runs.
- **Routers**: each router with state, routing list, last sync (result, when), drift and unmanaged tags. The router page holds the connection (with **Test connection**), installed tags against desired ones, **Preview sync**, **Sync now**, the sync history with plans and logs, and activity.
- **Shadowrocket configs**: each config with list, URL and last fetch. The config page has the base config editor with version history, a **preview** of the rendered output, the URL (copy, QR, regenerate) and the fetch log.

## Ports for other modules

| Port | Used by | Contract |
|---|---|---|
| `RouterRegistrar` | router scripts | Register a router in `awaiting setup` with a name, routing list and connection. Return the address-list and DoH-forwarder names it must use. |

| `RoutingGuard` | servers (provisioning) | `Covering(ctx, hostnames)`: for each hostname a listed name covers (any listed name, installed or not), the name, its service and its list. `Module.Guard()`, set with `srv.SetRouting(rt.Guard())`. |

It consumes `ServerHostnames` (the guard) and `ProxyDialer` (discovery through a server) from servers.

## Events

`routing.*`: see [events](../events.md#routing).
