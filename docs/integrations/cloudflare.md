# Cloudflare (DNS provider)

The first DNS driver. Proxier creates the A records of servers it provisions, waits for them to resolve, and deletes them at retirement. It never touches records it didn't create.

## Setup

- Settings → Integrations → Cloudflare: an **API token** with `Zone → DNS → Edit` (and `Zone → Zone → Read`) for the zones Proxier may use, e.g. `tikhonnnnn.com`. Saving the token first verifies it and lists the zones with it; if Cloudflare refuses, nothing is saved ("Cloudflare refused this token"). **Test** lists the zones the token can see again. The admin ticks the ones Proxier may use (`cloudflare.zones`, comma-separated names; a name the token does not see is dropped).
- The hostname pattern (Settings → Servers) must end in one of those zones. Proxier picks the zone by the longest matching suffix. Settings → Servers warns when the pattern's zone is not ticked.

## Records

| Field | Value |
|---|---|
| Type | `A` (IPv4 only in the first version) |
| Name | the management hostname (and the proxy hostname, once different) |
| Content | the server's IP |
| Proxied | **false, always.** Clients connect straight to nginx with the server's own certificate, and Let's Encrypt must reach port 80. A proxied record would break both. |
| TTL | 60 s |
| Comment | `proxier:<server name>`. This marks the record as Proxier's. |

## Behaviour

- **Create**: if a record already exists at the name, it is updated only if it is the only one and its comment is Proxier's for the same server (a retry; a record someone proxied or gave another TTL is put back to DNS-only, 60 s). Otherwise provisioning stops with a conflict that shows the existing value (several records: all their values) and its comment, and **Overwrite** is an explicit admin choice ([provisioning](../processes/servers/server-provisioning.md)): the first record is updated in place and given Proxier's comment, and any others at the name are deleted so the name leads to one IP.
- **Wait**: after writing, Proxier asks Cloudflare (`1.1.1.1`) and Google (`8.8.8.8`) every 10 s, until both answer with the IP or 10 min pass. The questions go **over DNS-over-HTTPS** (RFC 8484, at `https://1.1.1.1/dns-query` and `https://8.8.8.8/dns-query`, addressed by IP so no name needs resolving), because Proxier sits behind the home router, which may intercept port 53. A resolver whose DoH request fails at the network level (or answers an HTTP error) is asked over plain UDP port 53 instead, and the job log says which transport answered. An NXDOMAIN is an answer ("nothing yet"), not a failure. After the timeout the error names what each resolver said. Cancelling ends the wait at once.
- **Delete** at retirement: only a record with Proxier's comment for that server that still points to its IP; otherwise it is left and the reason shows ("points to 198.51.100.7", "comment changed"). A record that is already gone counts as removed.
- The Cloudflare record ID is stored with the server's DNS record, so later changes don't search by name.

## Errors

| Cloudflare says | Proxier does |
|---|---|
| `401` / `403` | The step fails with "Cloudflare token rejected or lacks DNS edit on <zone>". Settings shows the token as failing. |
| Zone not found | The form refuses the server before anything starts (the hostname isn't in an allowed zone). |
| `429` | Waits what `Retry-After` asks (at most 60 s; absent or larger means 10 s) and retries within the call, up to 5 times. |
| `5xx` or a network error | Retries 3 times within the call, after 1 s, 3 s and 9 s, then fails the step. **Retry** resumes here. |

Every call has a 15 s timeout, and the token never appears in an error or a log. The settings are `cloudflare.api_token` (a secret, sealed with the vault) and `cloudflare.zones`; the driver reads both on every call, so a change applies at once.
