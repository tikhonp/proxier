# Service management

**Actors**: Admin; routing module; the sources; the sync engine (reacting to changes).

A service is a named set of domains from one source, and the unit that goes into routing lists and onto routers under its tag. Upstream services (v2fly, iplist, URL) are fetched. Custom services are written in Proxier and replace your own domain-list files on copyparty.

## Steps — adding an upstream service

1. Routing → Services → **Add service**, or **Add to lists…** from search or discovery. The admin enters or picks a selector:
   - `v2fly:<name>`, `iplist:<site|group>`, `iplist:<portal>:<site|group>`, `https://…`, or `<tag>=https://…`;
   - a bare name, rewritten as `v2fly:<name>`.
2. Proxier parses the selector and computes its **tag** without the network ([domain sources](../../integrations/domain-sources.md#tags)).
3. **The tag is already taken** by another service → no new service. The dialog shows the existing one and offers:
   - **Switch source**: the existing service gets this selector. Its next snapshot comes from the new source, and the routers re-sync that tag.
   - For a URL: **choose another tag** (`mine=https://…`), which makes a separate service.
4. **The tag is free** → Proxier resolves the selector once:
   - **v2fly**: a `404` means "v2fly has no list `<name>`", with a link to search.
   - **iplist**: tries the portals in order. Every reachable portal answering empty means "not found". An unreachable portal is reported as unreachable, never as "not found".
   - **URL**: an HTTP error is reported.
5. The parsed result becomes the first snapshot (suffix and exact domains, skipped entries) and the service is created. If routing lists were chosen, it is added to them, which triggers sync. → `routing.service_added{selector, tag}`

## Steps — custom services

1. Routing → Services → **New custom service**: name, tag (a slug of the name, editable), description.
2. The **domain editor** is a table of domain, **suffix** or **exact**, and note. Two ways to add domains:
   - **Paste many**: one per line. Accepts v2fly-like lines (`domain:`, `full:`, plain = suffix) and full URLs (the host is taken).
3. Every domain is normalised:
   - lowercased and trimmed;
   - scheme, path, port and a leading `*.` removed (`*.example.com` means suffix `example.com`);
   - internationalised names converted to punycode.

   Then validated: IP addresses are refused (services hold domains only), and so are invalid names. Duplicates are merged, and a suffix entry absorbs exact entries under it, with a note.
4. Each entry shows **already covered by** when another service in the same routing lists has it or a suffix above it (informational).
5. **Save** writes the domains and a new snapshot, then the targets of its lists re-sync. → `routing.service_added` (first save) or `routing.service_updated{added, removed}`
6. Saving is refused if any domain would trip the [server hostname guard](./routing-lists.md#rules) in a list the service is in.

## Steps — the service page

1. **Overview**: selector, source, portal, tag, counts, last check, last error, consecutive failures, and the lists it's in.
2. **Domains** of the current snapshot: filterable, suffix and exact marked, the skipped entries with reasons, and which domains another service owns in each list ([ADR 0013](../../adr/0013-one-owner-service-per-domain.md)).
3. **History**: snapshots (accepted, rejected, superseded) with counts and a diff against the previous accepted one. A **rejected** snapshot has **Accept anyway** ([upstream refresh](./upstream-refresh.md)).
4. Actions:
   - **Refresh now**, which runs the refresh flow for this service;
   - **Switch source** (upstream);
   - **Edit domains** (custom);
   - **Rename tag** (custom), which makes routers drop the old tag and install the new one;
   - **Remove**, only when it is in no routing list.

   → `routing.service_removed`

## Rules

- Exactly one service per tag. Tags are never empty: a selector whose tag would be empty is refused.
- Sources are explicit. Proxier never guesses one, and bare names always mean v2fly.
- Snapshots hold domains only. `regexp:` and `keyword:` lines, `@attributes`, IPs and invalid names are dropped and listed as skipped.
- Upstream services never change between refreshes, except through **Refresh now** or **Switch source**. A custom service changes only when saved.
- Removing a service from a list doesn't delete it. Deleting needs it out of every list.
- Changing a service never touches a router directly: it marks the affected targets for sync.

## Edge cases (each is a test)

- Add `anthropic` → stored as `v2fly:anthropic`, tag `anthropic`.
- Add `iplist:claude.ai` while `v2fly:anthropic` exists → tag `claude.ai`, a separate service.
- Add `anthropic` while `v2fly:anthropic` exists → no new service. The dialog shows the existing one.
- Add `iplist:youtube.com` → it's on main, so `iplist:youtube.com`, tag `youtube.com`.
- Add `v2fly:nosuchlist` → "v2fly has no list 'nosuchlist'", nothing created.
- Add `iplist:example.org` while the beta portal is down and main/russia answer empty → "not found on main, russia; beta unreachable", nothing created.
- Add `https://files.example.com/share/x/tunneled-domains.txt` → tag `tunneled-domains`. Add `mine=https://…/list.txt` → tag `mine`.
- Custom paste `https://www.Example.com/path`, `*.cdn.example.net`, `full:api.example.org`, `1.2.3.4` → suffix `www.example.com`, suffix `cdn.example.net`, exact `api.example.org`, and `1.2.3.4` refused as an IP.
- Custom domains `example.com` (suffix) and `full:api.example.com` → the exact entry is absorbed, with a note.
- `пример.рф` → stored as `xn--e1afmkfd.xn--p1ai`, shown with its Unicode form.
- A custom service with `tikhonnnnn.com` in a list (it covers `nl-1.hosts.tikhonnnnn.com`) → save refused, naming nl-1.
- Rename custom tag `mine` to `personal` → routers drop `mine` and install `personal` on the next sync.
- **Remove** a service that is in "Main" → refused until it is taken out of "Main".
- **Switch source** of `iplist:apple` to `iplist:beta:apple` → allowed (same tag `apple`). The next snapshot comes from beta.
- **Switch source** of `anthropic` to `iplist:claude.ai` → refused: that selector's tag is `claude.ai`. A switch keeps the tag, so it only accepts selectors with the same tag, such as `https://…/anthropic.txt` or `anthropic=https://…`.
