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

A tag is never empty: an empty tag is what untagged router entries carry. It never equals `telegram-cidr` and never starts with `mtvpn:`.

## v2fly

- Lists: `https://raw.githubusercontent.com/v2fly/domain-list-community/refs/heads/master/data/<name>`. A `404` means the list doesn't exist.
- Names (catalog): the git trees API (`/repos/v2fly/domain-list-community/git/trees/master`, then the `data` tree). The contents API truncates at 1000 entries.
- Reverse index: the repository archive at `master`, parsed in full.
- Format, line by line, with comments (`#`) stripped:

| Line | Meaning |
|---|---|
| `domain:example.com`, or a plain `example.com` | suffix domain |
| `full:api.example.com` | exact domain |
| `include:other` | the other list, resolved relative to `data/`, recursively, each list once |
| `regexp:…`, `keyword:…` | skipped (RouterOS can't express them), reported |
| `… @attr @attr2` | attributes dropped |

- A name that is both suffix and exact is kept as suffix only.
- Names failing `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$` are dropped and reported.
- An `include:` that fails fails the whole resolve. A partial list is never used.

## iplist (iplist.opencck.org)

- Three near-disjoint portals, tried in this order: `main` (`https://iplist.opencck.org/`), `beta` (`https://beta.iplist.opencck.org/`), `russia` (`https://russia.iplist.opencck.org/`). `iplist:<portal>:<sel>` pins one.
- Query: `?format=text&data=domains&wildcard=1&site=<sel>` or `&group=<sel>`. Site is tried before group, because a site name can be dotless, so the shape doesn't tell them apart.
- **`wildcard=1` is mandatory.** `wildcard=0` returns every hostname ever seen (15,000+ for youtube). The wildcard set is the apex list, exactly what `match-subdomain=yes` means.
- **A miss is `200` with an empty body**, not `404`. So an unreachable portal must never count as a miss: "not found" requires every portal in scope to answer empty.
- The output is a plain list of suffix domains. The domain-name check drops the scraped junk it sometimes carries.
- Catalog: `?format=custom&data=domains&wildcard=1&template={group}|{site}|{data}` per portal. It prints one line per domain (`{data}` is the selected data, the domain itself: verified 2026-10-08), so each line gives a (portal, group, site) triple and a domain of that site for the reverse index.

## URL

- Any `http`/`https` URL whose body is in v2fly format (plain lines are suffix domains). That includes your own lists anywhere.
- Fetched with a 30 s timeout and the user agent `proxier/<version>`. HTTP errors fail the resolve.
- `include:` inside a URL list resolves relative to the URL's directory.

## Custom

- Domains stored in Proxier and edited in the UI. No fetching. The snapshot is written on save.
- Input is normalised ([service management](../processes/routing/service-management.md#steps--custom-services)): lowercase, scheme/path/port stripped, `*.` meaning suffix, internationalised names to punycode, IPs refused.
