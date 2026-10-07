# Shadowrocket config

**Actors**: Admin; Shadowrocket on the phone (through `GET /r/{token}/{name}.conf`); routing module.

The phone, away from home, should tunnel the same domains as the home router. Proxier hosts a Shadowrocket config for that: the admin's hand-written base config, with the routing list's rules spliced into `[Rule]`. Shadowrocket subscribes to its URL once, and every routing change reaches the phone on its next refresh. This replaces `mtvpn.py shadowrocket` and the copyparty upload. Format rules: [Shadowrocket integration](../../integrations/shadowrocket.md).

## Steps — creating

1. Routing → Shadowrocket → **New config**: name (also the file name, e.g. `iphone`), routing list, rule policy (default `PROXY`), and the base config (paste, upload, or **Import from URL**, which reads it once, e.g. from copyparty).
2. The base config is validated: it must have a `[Rule]` section. Otherwise saving is refused, with "no [Rule] section to add the services to".
3. Saving creates base config version 1 and the token, and shows the URL `https://proxier.tikhonnnnn.com/r/{token}/iphone.conf` with **Copy** and a **QR code**. → `routing.shadowrocket_created`
4. In Shadowrocket: Config → add the URL, then use it.

## Steps — serving

1. Rate limit per IP as for links. An unknown token → `404`.
2. Render:
   1. Take the base config verbatim.
   2. Find `[Rule]`. Rules go in before its `FINAL,` line if it has one (Shadowrocket stops at the first match), otherwise at the end of the section, followed by `FINAL,DIRECT`.
   3. Write a header comment, `# Services from routing list "Main", by Proxier (2026-10-05 04:12 UTC)`, then one `# <tag>` block per service in list order, with `DOMAIN-SUFFIX,<d>,<policy>` for suffix domains and `DOMAIN,<d>,<policy>` for exact ones. Apply the list's ownership rules ([routing lists](./routing-lists.md#rules)). A service left with no rules gets no block.
3. Answer `200` with `Content-Type: text/plain; charset=utf-8` and `Cache-Control: no-store`.
4. Record the fetch: time, IP, user agent.

## Steps — editing

1. **Edit base config** opens the editor. Saving validates and creates a new base version. Older versions can be viewed, diffed and restored (a restore creates a new version). → `routing.shadowrocket_updated`
2. **Preview** shows the rendered output exactly as the phone would get it.
3. **Change routing list**, **change rule policy**: from the next fetch on.
4. **Regenerate token**: the old URL returns `404`, and the new URL is shown.
5. **Disable**: the URL returns `404` until enabled.

## Rules

- The output is computed on every fetch from the current base version and the accepted snapshots. A sync never needs to run.
- Everything outside the inserted block is copied byte for byte from the base config, comments and blank lines included.
- The inserted block always comes before `FINAL`. Rules after `FINAL` would never match.
- The base config may hold `[Proxy]` or `[Proxy Group]` sections. Proxier copies them, but proxies normally come from the phone's link subscription, not from here.
- The server hostname guard applies, because the list is the same as for routers.

## Edge cases (each is a test)

- A base with `[General]`, `[Rule]` containing `RULE-SET,…` lines and `FINAL,DIRECT` → services inserted after the `RULE-SET` lines and before `FINAL,DIRECT`, all other lines unchanged.
- A base whose `[Rule]` has no `FINAL` and is followed by `[Host]` → services at the end of `[Rule]`, then `FINAL,DIRECT`, then a blank line before `[Host]`.
- A base without `[Rule]` → refused on save.
- "Main" with `anthropic` (suffix `anthropic.com`) and `mine` (exact `console.anthropic.com`) → only `DOMAIN-SUFFIX,anthropic.com,PROXY`, and `mine` gets no block if it has nothing else.
- Rule policy `Proxy-Group-A` → every inserted rule ends with `,Proxy-Group-A`.
- A snapshot accepted at 04:05 → the 04:06 fetch includes the change.
- Regenerate the token → the old URL `404`s, and the new one serves.
- A disabled config → `404`.
- Restore base version 2 while version 4 is current → version 5 is created with version 2's content.
