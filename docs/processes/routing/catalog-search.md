# Catalog search

**Actors**: Admin; the catalog refresh job (queue `refresh`, daily 04:30 and on demand); GitHub (v2fly); the iplist portals.

The catalog lets the admin find services without knowing selectors by heart. It is mtvpn's `search`, kept locally and searched instantly. It also feeds discovery's catalog lookup through a reverse index from domains to the selectors that contain them. Source details: [domain sources](../../integrations/domain-sources.md).

## Steps — refresh

1. **v2fly commit**: one GitHub API request (`repos/v2fly/domain-list-community/commits/master`, `Accept: application/vnd.github.sha`) gives master's commit. When it is the commit of the catalog in force, nothing is downloaded.
2. **v2fly names and reverse index**: download the repository archive at that commit (at `master` when the API failed). Every file under `data/` is a selector `v2fly:<name>`, the names come from the archive (the trees API isn't needed); every list is parsed and its includes resolved in memory with their filters, for its name count. Each direct suffix and exact entry is recorded with its list and attributes, and each include with its filter, so a lookup can follow them. The commit is recorded.
3. **iplist**: for each portal (main, beta, russia), request the custom export with the template `{group}|{site}|{data}`: one line per domain, so each gives a (portal, group, site) triple and a domain of that site for the reverse index. Groups get their site and domain counts. An export equal to the last one (SHA-256) is not written again.
4. Each source's new catalog is written as a new **generation** in short batches, then put in force in one short transaction, then the old generation is deleted; search answers from the old one until the switch. If a source fails, its previous catalog is kept and marked with its age; the other sources still refresh. → `routing.catalog_refreshed{v2fly, iplist_main, iplist_beta, iplist_russia}` (entries, −1 for a failed source), only when a source changed or failed. A source failing on 3 consecutive refreshes (days) → `routing.catalog_refresh_failed{source, error, since}` (notifies, once per run of failures)

## Steps — search

1. Routing → **Search**: a query box (substring match on the name, case-insensitive), with filters for source (v2fly / iplist), kind (v2fly list / iplist site / iplist group) and portal. v2fly lists come first, then iplist groups, then sites, each by name; at most 200 results ("N more: type more of the name").
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
- A site is tried before a group of the same name, so a group whose portal also has a site of that name can't be selected: its row says so and has no **Add**.
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
