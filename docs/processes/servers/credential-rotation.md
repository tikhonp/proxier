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
4. If any step fails before the commit, the server's files are restored to the last successful deployment (re-uploaded and redeployed) and the old values stay in force. → `server.redeploy_failed{kind: rotate, step, error}`

## Steps — cut-off (rotating for a link)

1. Link page → **Cut off**. The dialog lists every server in the link's subscription, each other link that serves any of them, and the warning above.
2. **Cut off** disables the link at once (it serves the "disabled" stub entry from now on) and queues a rotation per listed server. → `link.disabled`, `link.cut_off{servers}`
3. The rotations run like the single case, one server at a time, and each records its own events. The link page shows their progress.

## Rules

- Only values marked `rotate: true` change. Others (the container postfix, for example) stay.
- The old values stay in force until the new ones pass the proxy test, so a failed rotation never leaves a server unusable for everyone.
- A rotation is a deployment: it records a deployment and holds the server's resource key, so it never runs alongside a redeploy.
- Connection URIs are never cached anywhere in Proxier, so the next fetch of every link already carries the new credential.
- A cut-off disables the link even if a rotation then fails. The page shows which servers still have the old credential, each with **Retry**.

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
