# Router sync

**Actors**: Admin; router jobs (queue `routers`); the tailnet node; jump hosts; the MikroTik (RouterOS 7); routing module.

Router sync keeps a MikroTik's tunneled domains equal to its routing list: the DNS static FWD entries and the firewall address-list entries that mtvpn used to write, under the same tags. It runs by itself after every relevant change and repairs drift. It never touches what it doesn't own. The RouterOS details are in the [RouterOS integration](../../integrations/routeros.md).

## Steps — adding a router

1. Routing → Routers → **Add router**. Fields:
   - name and routing list;
   - **host** (e.g. the router's LAN address `10.230.1.1`), SSH port, user;
   - optional **jump host** (tailnet host, port, user);
   - **address list** (default `to_vpn_list`) and **DoH forwarder** (default `vpn-doh`).
2. The dialog shows Proxier's public key, with what to run on the jump host (append it to the user's `authorized_keys`) and on the router (add it to the router user's SSH keys).
3. **Test connection** runs a read-only job:
   1. Connect to the jump host, if set. On first contact, its host key fingerprint is shown and must be confirmed.
   2. Connect to the router through it. Its fingerprint is confirmed the same way.
   3. Read the RouterOS version and board.
   4. Check that the DoH forwarder and the infra pins (`mtvpn:doh`) exist.
   5. Count the entries in the address list.

   The result lists each check. A missing forwarder or pin gives the warning "this router doesn't look set up by the router script".
4. **Save** → the router is **active** and an initial sync is queued. → `routing.router_added`. The first successful connection → `routing.router_connected{version, board}`
5. A router created by router scripts starts **awaiting setup**: no syncs, no failure notifications. Proxier quietly tries to connect every 10 minutes for 7 days. The first success makes it active and runs the initial sync, as in step 4. After 7 days it stays awaiting setup and shows "never connected", with **Test connection**.

## Steps — sync

1. **Connect** through the jump host if set. Host keys must match their pins.
2. **Read** the router:
   - every DNS static entry with `address-list=<address list>`: comment, name, type, match-subdomain;
   - every non-dynamic entry of the address list: comment, address.

   Group them by comment (tag). Entries with an empty comment are **untagged**, and entries whose comment starts with `mtvpn:` are **infra pins**.
3. **Desired state**: per tag, the suffix and exact domains the routing list gives it after ownership ([routing lists](./routing-lists.md#rules)), minus every name that is an infra pin on this router (logged as skipped).
4. **Plan**, per tag:

   | Router holds | Proxier applied it before | Desired | Plan |
   |---|---|---|---|
   | exactly the desired names | yes or no | yes | **unchanged** (if never applied: recorded as applied, with no push) |
   | something else or nothing | yes or no | yes | **update** |
   | entries | yes | no | **remove** |
   | entries | no | no | **unmanaged**: reported, not touched |

5. **Push**, if the plan has updates or removals:
   - Updates of tags that gain names come first, then the other updates, then removals. A name moving between tags is never missing for longer than one block.
   - Each tag is one idempotent block. It removes the tag's entries, removes untagged duplicates of its names (adoption), and adds all names tagged ([RouterOS integration](../../integrations/routeros.md#service-block)).
   - Blocks are uploaded over SFTP as `proxier-sync-<job>-<n>.rsc` (at most 2,000 domains per file) and run with `/import file-name=… verbose=no`. The output is scanned for error patterns, because `/import` can report success on a failed line.
   - Each file is deleted afterwards, whether or not it succeeded.
6. **Verify**: re-read the counts of every touched tag and compare them with the plan.
7. **Record**: per tag, the applied hash and counts. The sync record keeps the plan, the result and the log. → `routing.router_synced{added, updated, removed}`
8. A failure at any step stops the job. Tags already pushed keep their new applied state, and the failed tag keeps its old one. → `routing.router_sync_failed{step, error, consecutive}`. Retries come after 5 min, 15 min and 1 h. Notified at the 2nd consecutive failure, or at once for **Sync now**. The next success after failures → `routing.router_recovered` (notifies).

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
2. **Drift check** (every 6 h, read-only) runs steps 1–4 and compares the router with the **applied** record, not the desired state. Pending desired changes have syncs of their own. Differences are drift → `routing.drift_detected{tags}`. With auto-repair on, a sync is queued. With it off, a notification is sent and the router page shows **Repair**.
3. **Unmanaged tags** appear on the router page with their entry counts, once each new set is found. → `routing.unmanaged_tags_found{tags}`. Actions per tag:
   - **Adopt**: offers `v2fly:<tag>` or `iplist:<tag>` if the catalog has them, or **Make a custom service from the router's entries**, then adds that service to the router's list;
   - **Remove from router** (confirmation);
   - **Ignore**: hidden from reports, left on the router.

## Steps — pausing and removing

1. **Pause** → no syncs, no drift checks. **Resume** → full sync.
2. **Remove router** asks: **Keep everything on the router** (default), or **Remove every tag Proxier installed**, which first runs a sync that removes them all. → `routing.router_removed`

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
- First sync of the mtvpn-managed home router, with "Main" imported from `mtvpn.yaml` → every matching tag recorded as unchanged, no push.
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
- The router is offline → the sync fails at connect, retries after 5 min, and the 2nd failure notifies. When it is back, `routing.router_recovered`.
- The container restarts during `/import` of a block → the job resumes and re-runs that block. The tag ends correct.
- The router's host key changed → the sync fails with "host key changed", nothing is pushed, and the security notification is sent.
- Switch the router from "Main" to "Parents" → the preview shows removals for tags missing from "Parents". Running it removes them.
- A router awaiting setup comes online on day 2 → active, `routing.router_connected`, initial sync.
- Remove a router with **Remove every tag Proxier installed** → a removal sync runs, then the router is removed from Proxier. Infra pins stay.
- A list with 6,000 domains → pushed in several files of at most 2,000 domains, all verified.
