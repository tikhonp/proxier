# Cloudflare (DNS provider)

The first DNS driver. Proxier creates the A records of servers it provisions, waits for them to resolve, and deletes them at retirement. It never touches records it didn't create.

## Setup

- Settings → Integrations → Cloudflare: an **API token** with `Zone → DNS → Edit` (and `Zone → Zone → Read`) for the zones Proxier may use, e.g. `tikhonnnnn.com`. **Test** lists the zones the token can see. The admin ticks the ones Proxier may use.
- The hostname pattern (Settings → Servers) must end in one of those zones. Proxier picks the zone by the longest matching suffix.

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

- **Create**: if a record already exists at the name, it is updated only if its comment is Proxier's for the same server (a retry). Otherwise provisioning stops with a conflict that shows the existing value, and **Overwrite** is an explicit admin choice ([provisioning](../processes/servers/server-provisioning.md)).
- **Wait**: after writing, Proxier asks `1.1.1.1` and `8.8.8.8` directly every 10 s, until both answer with the IP or 10 min pass.
- **Delete** at retirement: only records with Proxier's comment for that server that still point to its IP.
- The Cloudflare record ID is stored with the server's DNS record, so later changes don't search by name.

## Errors

| Cloudflare says | Proxier does |
|---|---|
| `401` / `403` | The step fails with "Cloudflare token rejected or lacks DNS edit on <zone>". Settings shows the token as failing. |
| Zone not found | The form refuses the server before anything starts (the hostname isn't in an allowed zone). |
| `429` | Waits and retries within the step. |
| `5xx` | Retries 3 times within the step, then fails it. **Retry** resumes here. |
