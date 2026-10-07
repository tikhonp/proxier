# Catalog search

**Actors**: Admin; the catalog refresh job (queue `refresh`, daily 04:30 and on demand); GitHub (v2fly); the iplist portals.

The catalog lets the admin find services without knowing selectors by heart. It is mtvpn's `search`, kept locally and searched instantly. It also feeds discovery's catalog lookup through a reverse index from domains to the selectors that contain them. Source details: [domain sources](../../integrations/domain-sources.md).

## Steps — refresh

1. **v2fly names**: read the repository's tree through the git trees API (the root tree, then `data/`), because the contents API truncates at 1000 entries. Each file under `data/` is a selector `v2fly:<name>`.
2. **v2fly reverse index**: download the repository archive at `master` and parse every list with `include:` resolved. Each suffix and exact domain is recorded with every list that contains it, directly or through an include. The commit is recorded.
3. **iplist**: for each portal (main, beta, russia), request the custom export with the template `{group}|{site}` (one line per domain, deduplicated) and record each (portal, group, site) plus each group's site count. Site names are domains (`youtube.com`), so they enter the reverse index as suffix domains of their site.
4. Each source's new catalog replaces its old one in one transaction. If a source fails, its previous catalog is kept and marked with its age. → `routing.catalog_refreshed{entries_per_source}`. A source failing on 3 consecutive days → `routing.catalog_refresh_failed{source, error, since}` (notifies)

## Steps — search

1. Routing → **Search**: a query box (substring match on the selector), with filters for source (v2fly / iplist), kind (v2fly list / iplist site / iplist group) and portal.
2. Each result shows:
   - the selector as it would be written (`v2fly:anthropic`, `iplist:apple`, `iplist:beta:cloudflare.com`);
   - kind and portal;
   - the group of a site, or the site count of a group;
   - **in lists** when it is already a service;
   - the catalog's age for its source.
3. Row actions:
   - **Preview**: fetch and parse now; show suffix and exact counts, the domains, and the skipped entries with reasons;
   - **Add to lists…**: create the service if needed, then add it to the chosen routing lists;
   - **Open service** when it exists.
4. No results → "Nothing in the catalog matches 'xyz'", with **Discover xyz.com** prefilled ([discovery](./domain-discovery.md)).

## Rules

- Search reads only the local catalog: instant, and it works when the sources are down. Preview and adding use the network.
- iplist selectors are added the way they will resolve. An unpinned `iplist:<sel>` resolves on the first portal (in order main, beta, russia) that has it. A result from a later portal is therefore added pinned (`iplist:beta:<sel>`) when an earlier portal also has that selector, so the service gets what the admin saw.
- v2fly names are matched as written. The UI never adds a bare name: it always writes `v2fly:`.
- An unauthenticated GitHub API allows 60 requests an hour, which is enough for a daily refresh. An optional GitHub token in Settings raises the limit.

## Edge cases (each is a test)

- Query "apple" → v2fly lists containing "apple", iplist group `apple`, and iplist sites containing "apple", each with portal and kind.
- Query "apple", filter iplist → only the iplist rows.
- `apple` exists as a group on main and on beta → two rows. Adding the beta row writes `iplist:beta:apple`. Adding the main row writes `iplist:apple`.
- A result already in "Main" → marked "in lists: Main", and **Add** offers the remaining lists.
- **Preview** of `v2fly:openai` → counts, domains, and skipped `regexp:` lines listed with "not supported on RouterOS".
- GitHub unreachable during refresh → the v2fly catalog is kept, with "3 days old" shown on its results.
- One iplist portal unreachable → that portal's entries are kept from the last refresh. The other portals refresh.
- No results for "kinopoisk" → the Discover suggestion with `kinopoisk.ru` prefilled.
- A refresh while a search is open → the search keeps working, and results update on the next query.
