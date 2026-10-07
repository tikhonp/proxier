# Upstream refresh

**Actors**: Scheduler; the refresh jobs (queue `refresh`, daily at 04:00 and on demand); the sources; the sync engine; the notifier.

Upstream lists change: v2fly adds a domain to `anthropic`, iplist reshuffles a group. Proxier picks the changes up every day and applies them automatically. Safety checks keep a broken upstream response (an empty file, a half-deleted list) from emptying routers, and one daily digest says what changed.

## Steps

1. At 04:00 (setting) the scheduler queues one refresh per upstream service (v2fly, iplist, URL) that is in at least one routing list. Custom services aren't refreshed: they change only when saved.
2. Each refresh fetches and parses the service's selector, exactly as when it was added ([service management](./service-management.md)).
3. **Fetch or parse error** (network, HTTP error, an `include:` that 404s, every iplist portal unreachable) → the current snapshot stays, and `last error` and `consecutive failures` are updated. At 3 consecutive failures → `routing.refresh_failing{failures, error}` (notifies).
4. **Same as the current snapshot** (same hash) → only `last checked` is updated.
5. **Different** → run the safety checks against the current accepted snapshot:

   | Check | Rejects when |
   |---|---|
   | Empty | The new snapshot has no domains at all. |
   | Shrink | The current snapshot has at least 20 domains and the new one has lost more than half of them (both thresholds are settings). |

6. **Passes** → the new snapshot is **accepted** and the old one becomes superseded. Every target whose routing list contains the service is marked for sync (coalesced, see [router sync](./router-sync.md)). → `routing.snapshot_accepted{added, removed, counts}`
7. **Fails** → the new snapshot is stored as **rejected** with the reason. The current one stays in force, and targets are not touched. → `routing.snapshot_rejected{reason, old_count, new_count}`
8. When the daily round is done, one **digest** notification summarises it: services changed with domains added and removed, rejections, failures. A rejection outside the daily round (from **Refresh now**) notifies on its own.

## Steps — handling a rejection

1. The service page shows the rejected snapshot with its diff against the current one, the reason, and **Accept anyway** / **Dismiss**.
2. **Accept anyway** makes it the accepted snapshot (as in step 6) and records who accepted it. → `routing.snapshot_accepted{added, removed, counts, forced: true}`
3. **Dismiss** leaves it rejected. The next refresh checks again from the current accepted snapshot.

## Rules

- The accepted snapshot is the only thing targets ever get. A rejected or failed refresh never changes a router or a Shadowrocket config.
- An unreachable iplist portal is never "not found": a selector that resolves only when all portals answer gets "every portal unreachable" as a failure, not an empty snapshot.
- A partial v2fly result is never accepted: if an `include:` fails, the whole refresh fails.
- Services in no routing list aren't refreshed on schedule (**Refresh now** still works).
- **Refresh now** on a service, and **Refresh all** on a routing list, run the same steps right away.
- Accepted snapshots are kept 90 days for diffs. The current one is kept for as long as the service exists.

## Edge cases (each is a test)

- v2fly adds 3 domains to `anthropic` → accepted, the routers with it re-sync, and the digest shows "+3".
- An unchanged list → `last checked` only, no new snapshot, nothing in the digest.
- A URL source returns an empty body with `200` → rejected "empty", the routers untouched, and listed in the digest.
- A list of 120 domains comes back with 50 → rejected "shrink 58 %". **Accept anyway** → accepted, marked forced, and the routers re-sync.
- A list of 12 domains comes back with 3 → accepted (under the 20-domain floor of the shrink check).
- A v2fly list whose `include:geolocation-cn` 404s → failure, not a partial snapshot.
- Network down at 04:00 → every refresh fails and the snapshots stay. On the third day, a `routing.refresh_failing` notification per service.
- iplist's beta portal is unreachable for `iplist:beta:apple` → failure, the snapshot stays.
- **Refresh now** produces a rejection at 15:00 → its own notification, not a digest.
- A service removed from every list → not refreshed at 04:00.
- Two services whose refreshes both change a router's domains → one coalesced sync of that router.
