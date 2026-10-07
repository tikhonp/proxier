# Server retirement

**Actors**: Admin; the retire job (queue `provisioning`); Cloudflare; the server (if reachable); subscriptions (reacting to the event).

Retirement takes a server out of service for good: it leaves every subscription, its checks stop, its DNS records go, and optionally its stack is removed from the VPS. The VPS itself is cancelled by the admin at the hosting provider; Proxier has no provider APIs. History stays: events, jobs, deployments, stats (until retention). The name stays taken forever.

## Steps

1. Server page → **Retire**. The dialog shows:
   - the subscriptions and number of links that serve the server ("12 links in 3 subscriptions will stop showing nl-1 on their next refresh");
   - the DNS records that will be deleted;
   - **Remove the stack from the server** (checked when the server is reachable): runs the template's `uninstall` steps and deletes `dir`;
   - a text field to type the server name to confirm.
2. **Retire** queues the job. The server stops being offered to subscriptions at once, and its checks stop.
3. Job steps:
   1. If provisioning is running for this server, cancel it and wait for it to stop.
   2. If chosen and the server is reachable over SSH, run the `uninstall` steps and remove `dir`. If the server is unreachable, log "skipped: unreachable" and continue.
   3. Delete each DNS record Proxier created for the server, provided it still points to the server's IP and still carries the comment `proxier:<name>`. A record that changed is kept and reported.
   4. Mark the server **retired** and erase its generated values. → `server.retired{dns_removed, stack_removed}`
4. Subscriptions react to `server.retired` by removing the server from every subscription. → `subscription.servers_changed{removed}` per subscription. From then on, link outputs no longer contain it.

## Rules

- Retirement can't be undone. A retired server can't be activated, upgraded or edited. To get a server there again, provision a new one (it gets a new number).
- Retired servers are hidden from the server list by default ("show retired" reveals them) and are never offered anywhere.
- Generated values are erased at retirement, so the old credentials can't be shown or served again. Deployed files are kept (encrypted) for history until the deployments are pruned.
- Proxier never deletes a DNS record it didn't create, or one that no longer points to the server.
- A server can be retired from provisioning, failed or active.

## Edge cases (each is a test)

- Retire an active, reachable server with the stack removal checked → `docker compose down -v`, `dir` deleted, DNS deleted, server retired, removed from its subscriptions, links no longer serve it.
- Retire an unreachable server → the stack step is skipped and logged. DNS deleted, server retired.
- The DNS record was changed by hand to point elsewhere → kept, and the event reports `dns_removed: false` for it.
- Retire a server whose provisioning is running → the provisioning job is cancelled first, then retirement continues.
- Retire a failed server whose provisioning stopped before DNS → no DNS to delete, server retired.
- Retire a server whose name is typed wrong in the confirmation → the button stays disabled.
- After retiring `nl-2`, a new server in `nl` → `nl-3`.
- A subscription whose only server is retired → it now has no servers, and its links serve "⚠️ No servers yet".
- A retired server's page → read-only, with history. No actions except viewing.
