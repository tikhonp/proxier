# Credential rotation

**Actors**: Admin; the rotation job (queue `provisioning`); the server; every link whose subscription includes the server.

Credentials are shared per endpoint ([ADR 0004](../../adr/0004-one-shared-credential-per-endpoint.md)), so the only way to take access away from someone who has a working connection URI is to change the credential for everyone. Rotation replaces a server's **rotatable generated values** (in the seed template, the client UUID and the XHTTP path) and redeploys. Links serve the new connection URIs at once, because outputs are computed on every fetch. Apps that refresh keep working. Anything holding only the old URIs stops.

## Steps — rotating one server

1. Server page → **Rotate credentials**. The dialog says:
   - which values will change;
   - how many links (and in which subscriptions) serve this server;
   - "Apps refresh within their update interval (12 h by default). Until an app refreshes, it can't connect to this server. Anyone using a copied URI or a disabled link loses access."
2. **Rotate** queues the job.
3. Job steps:
   1. Generate new values for every rotatable generated value, held next to the old ones.
   2. Render with the new values and upload (`upload-files`).
   3. Run the version's `redeploy` steps.
   4. Proxy test through every endpoint **with the new values**.
   5. Commit: the new values replace the old ones, and the endpoints are stored with the new credential and parameters. → `server.credentials_rotated{keys}` (notifies)
4. If a step fails before the commit, the **restore runs inside the failing step**, before the job reports the failure: the server's current files are uploaded again (a deployment of kind `restore`), the redeploy steps run, the proxy test passes with the old values, and the new values are dropped. The old values stay in force throughout. → `server.redeploy_failed{kind: rotate, step, error, restored}`; `restored` is false when the restore failed too, and the log says what may be on the server.
5. If the admin cancels after the new values exist, the cancellation queues a `servers.restore` job (the rotation's log says "restore queued as job #N") that does the same.

## Steps — cut-off (rotating for a link)

1. Link page → **Cut off…** (in the Actions area, and in the shared-link alert band). The page lists every server of the link's subscription in order with its health, the ones that can't be rotated with why ("not in service", "its template has no rotatable values"), how many other active links in how many subscriptions get new URIs, and the warning above. A subscription with nothing to rotate says so: Cut off only disables the link.
2. **Cut off** disables the link at once (it serves the "disabled" stub entry from now on; an already disabled link stays as it is), skips and names the servers that can't be rotated, and queues the first rotation, all in one transaction. → `link.disabled` (when it was active), `link.cut_off{servers, skipped}`
3. The rotations run like the single case, one server at a time: an event subscriber (`subscriptions.cutoff`) starts the next one when the previous one records `server.credentials_rotated` or `server.redeploy_failed`. A failure doesn't stop the rest. A server that stops being rotatable while it waits is skipped when its turn comes. The link page's **Cut-off** area shows their progress (waiting, rotating with its job, rotated, failed or skipped with "still has the old credential") and polls every 2 s while one waits or runs.
4. **Retry** on a failed server queues it again: at once when nothing runs, else after the running one.

## Rules

- Only values marked `rotate: true` change. Others (the container postfix, for example) stay.
- The old values stay in force until the new ones pass the proxy test, so a failed rotation never leaves a server unusable for everyone.
- A rotation is a deployment: it records a deployment and holds the server's resource key, so it never runs alongside a redeploy.
- Connection URIs are never cached anywhere in Proxier, so the next fetch of every link already carries the new credential.
- A cut-off disables the link even if a rotation then fails. The page shows which servers still have the old credential, each failed one with **Retry** (a skipped one has none).
- A second cut-off while one runs is allowed; each has its own servers and the page shows the newest. Two rotations of one server queue behind each other on its resource key.

## Edge cases (each is a test)

- Rotate `nl-1` → the UUID and path change. The old connection URI fails the proxy test, and the new one passes.
- Fetch of any link that includes `nl-1` right after the commit → the new URI.
- Fetch during the job, before the commit → the old URI (still in force).
- Upload of the new files fails → the old files are restored, the old values stay, and the event names the step.
- The proxy test with the new values fails → the old files are restored and redeployed. Afterwards the proxy test with the old values passes.
- Rotating a server that is in no subscription → allowed. The dialog says no links are affected.
- Cut off a link whose subscription has three servers → the link is disabled at once, three rotations run one after another, and three `server.credentials_rotated` events are recorded.
- Cut-off where the second server is unreachable → the link stays disabled, server 1 is rotated, server 2 shows the failure with **Retry**, and server 3 still runs.
- Rotation requested while a redeploy of the same server runs → it waits and then runs.
