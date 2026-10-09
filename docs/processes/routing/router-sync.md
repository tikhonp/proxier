# Router sync

**Actors**: Admin; router jobs (queue `routers`); the tailnet node; jump hosts; the MikroTik (RouterOS 7); routing module.

Router sync keeps a MikroTik's tunneled domains equal to its routing list: the DNS static FWD entries and the firewall address-list entries that mtvpn used to write, under the same tags. It runs by itself after every relevant change and repairs drift. It never touches what it doesn't own. The RouterOS details are in the [RouterOS integration](../../integrations/routeros.md).

## Steps — adding a router

1. Routing → Routers → **Add router**. Fields:
   - name and routing list;
   - **host** (e.g. the router's LAN address `10.230.1.1`), SSH port, user;
   - optional **jump host** (tailnet host, port, user);
   - **first hop**: direct, or through Proxier's tailnet node (the default while the node runs);
   - **address list** (default `to_vpn_list`) and **DoH forwarder** (default `vpn-doh`).
2. The dialog shows Proxier's public key, with what to run on the jump host (append it to the user's `authorized_keys`) and on the router (add it to the router user's SSH keys).
3. **Test connection** runs a read-only job:
   1. Connect to the jump host, if set. On first contact, its host key fingerprint is shown and must be confirmed.
   2. Connect to the router through it. Its fingerprint is confirmed the same way.
   3. Read the RouterOS version and board.
   4. Check that the DoH forwarder and the infra pins (`mtvpn:doh`) exist.
   5. Count the entries in the address list.
   6. Upload a one-line file over SFTP and delete it (a user whose group lacks `ftp` fails here, not at the first sync).

   The result lists each check. A missing forwarder or pin gives the warning "this router doesn't look set up by the router script"; Save is still allowed. **Stop** at a fingerprint ends the test and pins nothing.
4. **Save** → the router is **active**. → `routing.router_added`. When the latest test of exactly these values passed (or warned), the router is connected → `routing.router_connected{version, board}`, and its initial sync is queued. Save also works **untested**: the router then waits ("Not connected yet: Test connection to confirm the host keys; the initial sync runs after it passes."), changes queue nothing for it, and its first passing test records `routing.router_connected` and queues the initial sync. **Sync now** on it is allowed and fails with "first contact with 10.230.1.1:22: confirm its fingerprint with Test connection".
5. A router created by router scripts (`routers.Register`, wrapped by Phase 4's `RouterRegistrar`) starts **awaiting setup**: no syncs, no failure notifications. Proxier quietly tries to connect every 10 minutes for 7 days (`routing.probe`). A probe pins the router's own key on first contact (nobody can confirm a fresh router's key), but its jump host must already be confirmed: a probe never pins a jump host. The first success makes it active and runs the initial sync, as in step 4. A failed probe records nothing; the router page shows its error. After 7 days it stays awaiting setup and shows "never connected", with **Test connection**, whose pass (the admin confirms both keys) activates it the same way.

## Steps — sync

1. **Connect** through the jump host if set. Host keys must match their pins. A router no longer active (or gone) ends the job quietly. The first successful connection ever → `routing.router_connected`.
2. **Read** the router (every attempt and every resume starts here, so a retry never pushes a stale plan; files a cut-short run left are deleted first):
   - every DNS static entry with `address-list=<address list>`: comment, name, match-subdomain, type, forward-to;
   - every non-dynamic entry of the address list: comment, address.

   Group them by comment (tag). Entries with an empty comment are **untagged**, and entries whose comment starts with `mtvpn:` are **infra pins**.
3. **Desired state**: per tag, the suffix and exact domains the routing list gives it after ownership ([routing lists](./routing-lists.md#rules)), minus every name that is an infra pin on this router (logged as skipped).
4. **Plan**, per tag:

   | Router holds | Proxier applied it before | Desired | Plan |
   |---|---|---|---|
   | exactly the desired names | yes | yes | **unchanged** |
   | exactly the desired names | no | yes | **record**: recorded as applied, nothing pushed |
   | something else or nothing | yes or no | yes | **update** (why: *new*, *changed*, or *drift* when the desired state is what was applied) |
   | entries | yes or no | yes, owning nothing in the list | **remove** |
   | entries | yes | no | **remove** (it left the list) |
   | entries | no | no | **unmanaged**: reported, not touched |
   | nothing | yes | yes owning nothing, or no | **forget**: the applied record goes |

   "Exactly" is strict: the DNS entries' (name, match-subdomain) equal the desired (name, suffix or exact) set, each is `type=FWD` to the forwarder, and the tag's address-list names equal the desired names. A wrong forwarder, a wrong match-subdomain, a missing address-list name or a duplicate entry makes the tag differ.

5. **Push**, if the plan has updates or removals:
   - Updates of tags that gain names come first, then the other updates, then removals. A name moving between tags is never missing for longer than one block.
   - Each tag is one idempotent block. It removes the tag's entries, removes untagged duplicates of its names (adoption), and adds all names tagged ([RouterOS integration](../../integrations/routeros.md#service-block)).
   - Blocks are packed, in that order, into files `proxier-sync-<job>-<n>.rsc` of at most 2,000 domains (a bigger block is split, its later parts continued), uploaded over SFTP with a plain write and run with `/import file-name=… verbose=no`. The output is scanned for error patterns, because `/import` can report success on a failed line.
   - Each file is deleted afterwards, whether or not it succeeded. After a file succeeds its tags are recorded as applied; after one fails, the router is read again and each of its tags that landed anyway is recorded.
6. **Verify**: re-read the router; every pushed tag must now match (updates) or hold nothing (removals), else the job fails at verify ("openai: 29 names on the router, 31 expected").
7. **Record**: per tag, the applied hash and counts. The sync record keeps the plan, the result and the log. → `routing.router_synced{added, updated, removed, recorded, trigger}` when anything was pushed or recorded; a sync with nothing to do records no event.
8. A failure at any step stops the attempt. Tags already pushed keep their new applied state, and the failed tag keeps its old one. Every failed attempt is recorded → `routing.router_sync_failed{step, error, consecutive, manual, attempt, final}`. Retries come after 5 min, 15 min and 1 h. Only the **final** failure notifies: the fourth attempt, or an error no retry fixes (a refused key, a changed or unknown host key, the tailnet off), also for **Sync now**. The next success after failures → `routing.router_recovered`, which notifies only after a notified failure.

## Triggers

| Trigger | Sync |
|---|---|
| A service added, removed or reordered in the router's list; a snapshot of one of its services accepted; a custom service in it saved; the router's list switched | queued with a 30 s delay, coalesced per router |
| **Sync now** | queued at once |
| A drift check finding drift, with auto-repair on (default) | queued at once |
| An unmanaged tag adopted or removed | queued at once |
| Router resumed from paused | queued at once (full) |

## Steps — preview, drift, unmanaged tags

1. **Preview sync** runs steps 1–4 only (read-only). It shows the plan per tag (unchanged / update with +added −removed / remove / unmanaged) and the exact RouterOS scripts, with **Run this sync**.
2. **Drift check** (every `routing.drift_every`, 6 h by default, read-only) reads the router and compares it with the **applied** record, not the desired state. Pending desired changes have syncs of their own. A tag drifts when it is gone, its DNS entries' names differ from the applied hash, an entry isn't `FWD` to the forwarder, or its address-list count differs. Differences are drift → `routing.drift_detected{tags, repair}`. With auto-repair on, a sync is queued at once. With it off, a notification is sent once per set of drifting tags (the same drift found again records nothing) and the router page shows **Repair**. The round skips a router with a job queued or running (that job reads it anyway), and paused, awaiting and removing routers. A check that can't connect fails only its own row: no failure is counted and no event recorded.
3. **Unmanaged tags** appear on the router page with their entry counts, once each new set is found. → `routing.unmanaged_tags_found{tags}`. Actions per tag:
   - **Adopt**: offers `v2fly:<tag>` or `iplist:<tag>` if the catalog has them, or **Make a custom service from the router's entries**, then adds that service to the router's list;
   - **Remove from router** (confirmation): a sync now that removes only that tag;
   - **Ignore**: hidden from reports and notifications, left on the router; **Stop ignoring** brings it back without a notification.

   A tag that isn't a valid tag (a space, say) can't be adopted: only remove and ignore are offered. A tag that leaves the router and comes back notifies again (an ignored one too: ignoring holds while the tag is seen).

## Steps — pausing and removing

1. **Pause** → queued syncs, previews and drift checks are cancelled; no syncs, no previews, no drift checks. **Resume** → full sync. → `routing.router_paused`, `routing.router_resumed`
2. **Remove router** asks: **Keep everything on the router** (default; the router is removed at once), or **Remove every tag Proxier installed**: the router is *removing* and `routing.remove` removes every applied tag (infra pins, untagged and unmanaged entries stay), then deletes the router. → `routing.router_removed{name, cleaned}`. Its failures are recorded like a sync's and notify when the job gives up; the router stays *removing* (no syncs) with **Retry** and **Remove without cleaning**. Pinned host keys stay (Settings → SSH forgets them).

## Rules

- Proxier only touches entries in the router's address list and the DNS static FWD entries that point at it. It never touches infra pins (`mtvpn:…`), dynamic entries, other address lists or other DNS entries ([ADR 0010](../../adr/0010-router-contract-stays-mtvpn-compatible.md)).
- A tag Proxier never installed is never removed without the admin choosing **Remove from router**.
- Untagged entries are adopted only when a desired tag includes their name. Other untagged entries are left alone and counted on the router page.
- Names that are infra pins on the router (e.g. `dns.google`, `core.telegram.org`) are never installed as service entries. Installing `dns.google` as a FWD entry through the DoH forwarder would make the forwarder depend on itself.
- One job per router at a time. Changes coalesce into one sync, 30 s after the last trigger.
- Push timeouts are long (30 min per file). A client timeout in the middle of `/import` would leave a tag half-installed, and the block is idempotent, so the retry heals it.
- A tag already matching the router is never pushed, so the first sync of a router mtvpn manages changes nothing that matches.
- Routers in **awaiting setup** or **paused** are never synced.

## Edge cases (each is a test)

- Add a router behind a jump host, confirm both fingerprints → the test shows RouterOS version and board with no warnings. Save → initial sync.
- Decline the jump host's fingerprint → the test stops and nothing is pinned.
- A router without a `vpn-doh` forwarder → the "not set up by the router script" warning. Save is still allowed.
- First sync of the mtvpn-managed home router, with "Main" imported from `mtvpn.yaml` → every matching tag recorded (plan **record**), no push.
- Add `v2fly:openai` to "Main" → one sync about 30 s later with one update block for `openai`.
- Add five services within a minute → one sync with five update blocks.
- Remove `v2fly:openai` from "Main" → the next sync removes tag `openai`.
- Move `claude.ai` ownership from `anthropic` to `mine` (reorder) → `mine` is pushed before `anthropic`. The router has `claude.ai` at the end and in between.
- The router has tag `netflix` that Proxier never installed → listed as unmanaged and left alone. **Adopt** offers `v2fly:netflix`.
- Untagged `chatgpt.com` exists and `openai` (which has it) is installed → the untagged entry is replaced by a tagged one.
- An untagged `example.org` that no desired tag has → left alone and counted as untagged.
- 10 entries of tag `youtube` deleted by hand → the next drift check reports drift, and auto-repair reinstalls them.
- A service whose snapshot contains `dns.google` → that name is skipped on routers where it is an infra pin, and the skip is logged.
- `/import` output contains "syntax error" → the sync fails at push, the file is deleted, the failed tag keeps its old applied state, and the earlier tags keep their new one.
- The router is offline → the sync fails at connect, retries after 5 min, 15 min and 1 h, and only the fourth (final) failure notifies. When it is back, `routing.router_recovered` notifies. A sync that fails once and then succeeds recovers without a notification.
- The container restarts during `/import` of a block → the job resumes and re-runs that block. The tag ends correct.
- The router's host key changed → the sync fails with "host key changed", nothing is pushed, and the security notification is sent.
- Switch the router from "Main" to "Parents" → the preview shows removals for tags missing from "Parents". Running it removes them.
- A router awaiting setup comes online on day 2 → active, `routing.router_connected`, initial sync.
- Remove a router with **Remove every tag Proxier installed** → a removal sync runs, then the router is removed from Proxier. Infra pins stay.
- A list with 6,000 domains → pushed in several files of at most 2,000 domains, all verified.
- A router saved untested → no `router_connected`, no initial sync, and changes queue nothing for it; its first passing test connects it and queues the initial sync.
- A router user whose group lacks `ftp` → Test connection's upload check fails.
- A preview changes nothing on the router and records no event; a failed preview counts no failure.
