# Domain sources

The routing module's sources, with the rules carried over from `mtvpn.py`. Sources are pluggable: a new one implements resolve (selector → domains) and, if it has a catalog, list and index.

## Selector grammar

```
selector    = v2fly | iplist | url | named-url | bare
v2fly       = "v2fly:" name                       ; v2fly:anthropic
iplist      = "iplist:" [ portal ":" ] site-or-group  ; iplist:youtube.com, iplist:beta:apple
portal      = "main" | "beta" | "russia"
url         = scheme "://" …                     ; https://example.com/list.txt
named-url   = tag "=" url                         ; mine=https://example.com/list.txt
bare        = name                                ; anthropic  (= v2fly:anthropic)
```

- Prefixes are case-insensitive, and selectors are lowercased (URLs excepted).
- `<tag>=` only splits when a bare tag (`[a-z0-9][a-z0-9._-]*`) is directly followed by a URL scheme. A plain URL with `=` in its query is never split.
- Sources are always explicit. A bare name means v2fly and nothing else, and Proxier stores it as `v2fly:<name>`.
- Names: a v2fly list is `[a-z0-9][a-z0-9._!-]{0,62}` (v2fly has `geolocation-!cn`), an iplist site or group `[a-z0-9][a-z0-9._-]{0,252}` (sites are domains or dotless names like `copilot`).
- An unpinned iplist selector is stored as typed when it resolved on main, and **pinned** to the portal it resolved on otherwise (`iplist:apple` found only on beta is stored `iplist:beta:apple`), so a later refresh can't silently switch portals. The tag doesn't change.
- A selector that resolves to no name RouterOS can use (every line skipped) can't be added.

## Tags

The tag is computed without the network:

| Selector | Tag |
|---|---|
| `v2fly:<name>`, bare `<name>` | `<name>` |
| `iplist:<sel>`, `iplist:<portal>:<sel>` | `<sel>` |
| `<tag>=<url>` | `<tag>` |
| a URL with `site=` or `group=` in its query (an iplist URL) | that value |
| any other URL | the last path segment with `.txt`, `.list`, `.lst`, `.dat`, `.conf` or `.md` removed; the host if there's no path |
| custom service | chosen at creation (slug of the name) |

A tag is never empty: an empty tag is what untagged router entries carry. It never equals `telegram-cidr` and never starts with `mtvpn:`. Tags are `[a-z0-9][a-z0-9._!-]{0,62}` (RouterOS comments are written in double quotes, where `$`, `\` and `"` are special); a URL whose tag fails that is refused: name it, like `mine=https://…`. The URL tag is lowercased and not unescaped.

## v2fly

- Lists: `https://raw.githubusercontent.com/v2fly/domain-list-community/refs/heads/master/data/<name>`. A `404` means the list doesn't exist.
- Catalog: one API request for master's commit (`/repos/v2fly/domain-list-community/commits/master` with `Accept: application/vnd.github.sha`, plus `Authorization: Bearer <routing.github_token>` when set); when it changed, the archive `codeload.github.com/v2fly/domain-list-community/tar.gz/<commit>` (≤ 32 MiB, about 340 KB) gives every list name and the reverse index, parsed in full. The trees API isn't used.
- Format, line by line, with comments (`#`) stripped:

| Line | Meaning |
|---|---|
| `domain:example.com`, or a plain `example.com` | suffix domain |
| `full:api.example.com` | exact domain |
| `include:other` | the other list, resolved relative to `data/`, recursively, each list once, cycles ignored |
| `include:other @a @-b` | only the other list's entries that carry every `@a` and no `@-b` (the filter is honoured) |
| `regexp:…`, `keyword:…` | skipped (RouterOS can't express them), reported |
| `… @attr @attr2` on a plain entry | attributes dropped from the name (kept for include filters) |
| `… &list` | affiliations, ignored |

- A name that is both suffix and exact is kept as suffix only.
- Names failing `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$` are dropped and reported.
- An `include:` that fails (any HTTP error, `404` included) fails the whole resolve: `include:geolocation-cn: HTTP 404`. A partial list is never used.
- Limits: at most 300 lists and 16 MiB per resolve, 8 MiB per file.

## iplist (iplist.opencck.org)

- Three near-disjoint portals, tried in this order: `main` (`https://iplist.opencck.org/`), `beta` (`https://beta.iplist.opencck.org/`), `russia` (`https://russia.iplist.opencck.org/`). `iplist:<portal>:<sel>` pins one.
- Query: `?format=text&data=domains&wildcard=1&site=<sel>` or `&group=<sel>`. Site is tried before group, because a site name can be dotless, so the shape doesn't tell them apart.
- **`wildcard=1` is mandatory.** `wildcard=0` returns every hostname ever seen (15,000+ for youtube). The wildcard set is the apex list, exactly what `match-subdomain=yes` means.
- **A miss is `200` with an empty body**, not `404`. So an unreachable portal must never count as a miss: "not found" requires every portal in scope to answer empty.
- The output is a plain list of suffix domains. The domain-name check drops the scraped junk it sometimes carries.
- Catalog: `?format=custom&data=domains&wildcard=1&template={group}|{site}|{data}` per portal. It prints one line per domain (`{data}` is the selected data, the domain itself: verified 2026-10-08), so each line gives a (portal, group, site) triple and a domain of that site for the reverse index. Each portal is written as a new generation of the catalog; an export whose SHA-256 equals the one in force is not written again.

## URL

- Any `http`/`https` URL whose body is in v2fly format (plain lines are suffix domains). That includes your own lists anywhere.
- Fetched with a 30 s timeout and the user agent `proxier/<version>`. HTTP errors fail the resolve.
- `include:` inside a URL list resolves relative to the URL's directory, with the same filters and limits as v2fly. Redirects are followed up to 5 times; a body over 8 MiB is refused.

## Custom

- Domains stored in Proxier and edited in the UI. No fetching. The snapshot is written on save.
- Input is normalised ([service management](../processes/routing/service-management.md#steps--custom-services)): lowercase, scheme/path/port stripped, `*.` meaning suffix, internationalised names to punycode, IPs refused.
