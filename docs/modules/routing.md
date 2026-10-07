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

- Every service has a **tag**, which is its identity on routers (`comment=`) and is unique across services. It is the selector without the source prefix (`v2fly:anthropic` → `anthropic`). URL sources take the named tag, the iplist query value, or the file name without its extension. A custom service's tag is chosen at creation (a slug of its name) and can be renamed. A rename makes every router replace the old tag with the new one.
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
- **Ownership**: within a list, every name is installed once.
  - A name that another service's broader suffix domain covers is dropped.
  - A name that several services share goes to the first of them in list order.

  Removing, editing or reordering services moves ownership, and every affected tag is re-synced ([ADR 0013](../adr/0013-one-owner-service-per-domain.md)).
- **Server hostname guard**: a list can't contain a domain that equals or covers a server's management or proxy hostname (e.g. a custom service with `tikhonnnnn.com` would cover `nl-1.hosts.tikhonnnnn.com`). Routing those names would send Proxier's own checks, and the router's mihomo connection, into the tunnel. Saving is refused, naming the server and domain.

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
| State | `awaiting setup` (registered, never connected, no failure alerts), `active`, or `paused` (no syncs). | active, or awaiting setup when created by router scripts |

**Shadowrocket config**: a name, a routing list, a base config (versioned), a rule policy (default `PROXY`), and a secret URL `https://proxier.tikhonnnnn.com/r/{token}/{name}.conf`.

## Router sync

Sync makes a router hold exactly what its routing list says, per tag, using the RouterOS objects mtvpn uses ([RouterOS integration](../integrations/routeros.md)):

1. Read the router: every DNS static FWD entry in the address list, and every non-dynamic address-list entry, grouped by tag (comment).
2. Compute the desired domains per tag from the routing list (after ownership).
3. Plan per tag, comparing desired against what Proxier last applied and what the router actually holds: **unchanged** (skip), **update** (one idempotent service block: remove the tag, adopt untagged duplicates, add everything), or **remove** (a tag Proxier installed that is no longer desired).
4. Tags on the router that Proxier never installed are **unmanaged**. They are listed on the router page with **Adopt** (add the matching service to the list) and **Remove**, and never deleted on their own.
5. Push the script (upload + `/import`), scan the output for errors, verify the counts, and record what was applied.

Syncs are triggered by changes (30 s delay, coalesced per router), by **Sync now**, and by repair after a **drift check** (every 6 h, read-only; repair is automatic unless turned off). Entries commented `mtvpn:…` (infra pins) and dynamic entries are never touched. Details: [router sync](../processes/routing/router-sync.md).

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

It consumes `ServerHostnames` (the guard) and `ProxyDialer` (discovery through a server) from servers.

## Events

`routing.*`: see [events](../events.md#routing).
