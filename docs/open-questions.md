# Open questions

Decisions deliberately left open, and facts to verify during the build. When one is settled, move the answer into the relevant doc (and an ADR if it qualifies) and delete it here.

Settled on 2026-10-07:
- UI technology → templ + htmx ([ADR 0014](./adr/0014-server-rendered-ui-templ-htmx.md)).
- Proxy hostnames in certificate-transparency logs → an accepted risk for the first version, with central wildcard certificates planned ([servers module](./modules/servers.md#dns), [roadmap](./roadmap.md#later)).
- "Activate anyway" → yes ([provisioning](./processes/servers/server-provisioning.md#steps--after-a-failure)).
- Root or a deploy user → a `proxier` sudo user; root login is turned off ([provisioning](./processes/servers/server-provisioning.md)).
- Proxy test URL → configurable, globally and per template ([health](./processes/servers/server-health.md)).

## To decide

1. **`mtvpn:` naming.** Router entries and the router script keep the `mtvpn:` prefix and the `to_vpn_list` / `vpn-doh` names for compatibility ([ADR 0010](./adr/0010-router-contract-stays-mtvpn-compatible.md)). Rename to `proxier:` in a later router script version, with a migration sync?
2. **Zero-touch router registration.** A `@fill proxier-ssh-key` parameter in `fresh-router.rsc` could install Proxier's public key on the router during import, so it becomes syncable without manual key installation. This needs a small change to the script.
3. **Per-link credentials later.** If needed, the credential model becomes per subscription (one UUID per subscription per server) or per link. Templates are already written to iterate over a client list ([servers module](./modules/servers.md#future-per-link-credentials)), so this is mainly a sync job and data.
4. **Notification digest for bursts.** If many servers change at once (for example, a hosting network blocked), should they be grouped into one message? At the moment each server changes on its own, and a total failure from home is caught by the reference check.

## To verify during the build

1. **Client apps and the stub entry.** Which apps replace their list when a successful refresh returns one stub entry (expected: Happ, v2RayTun, Hiddify, Streisand, v2rayNG, Shadowrocket)? Which ones keep the old list on a `404`? This is the basis of [ADR 0004](./adr/0004-one-shared-credential-per-endpoint.md).
2. **Subscription headers** honoured per app: `profile-title`, `profile-update-interval`, `subscription-userinfo` (expire), `content-disposition` filename.
3. **check-host.net API**: endpoints (`/check-tcp`, `/check-result/{id}`, `/nodes/hosts`), node names per country, rate limits and terms of use for automated use.
4. **iplist custom export**: whether `format=custom` can emit the domain itself (for the reverse index), beyond the `{group}|{site}` template mtvpn uses.
5. **RouterOS**: SFTP vs SCP upload on the target versions, and the exact command to add an SSH public key from a string (shown to the admin in the router dialog).
6. **xray-core as a library**: ~~the binary size impact~~ (answered in 1c: v1.260327.0 embeds with `CGO_ENABLED=0`; the stripped binary grew from 47.3 to 62.4 MB, the image from 71 to 92.8 MB, and building it after adding xray took 17 s on the host with xray compiled from scratch and everything else cached; details in [1c](./build/1c.md#as-built)). Still open: the XHTTP `stream-up` client mode working through nginx `grpc_pass` exactly as the template's links expect. It is checked by the first provisioning on a real VPS (1d). The proxy test is proven against an in-process xray XHTTP server, which accepts any mode, so it says nothing about nginx. One more finding: xray's own XHTTP client has a data race (`splithttp.WaitReadCloser.Set`) that `go test -race` reports, so the packages that push XHTTP traffic are tested without `-race` ([Makefile](../Makefile)).
7. **tsnet with headscale**: whether the node joins headscale 0.29 with a pre-auth key, and whether it needs a non-expiring key. Built in 0e: the key is used only while the node has no login (the state in `/data/tailnet` keeps it across restarts), Settings → Integrations → Tailnet shows the key expiry and takes a new pre-auth key (never stored), and a node with saved state starts without `PROXIER_TS_AUTHKEY`. Not yet tried against the real headscale: join, restart, and what happens at expiry.
8. **Russian throttling of foreign hosting networks** (stalling after the first ~16–20 KB) as it applies to the chosen test payload size.
