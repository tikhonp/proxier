# One shared credential per endpoint; links control updates, rotation controls access

Every link that includes a server serves the same credential (the VLESS UUID) for each of its endpoints. Proxier never adds or removes per-person clients on servers. Disabling, expiring or deleting a link therefore stops its **updates**, not its access: the link then serves one stub entry with a `200`. Apps replace their list on a successful refresh, whereas on an error they would keep the old servers, so the stub is what actually clears the app. Real revocation is **rotation** of the servers' credentials, offered for a single link as **cut-off**. This was chosen for simplicity: nothing to sync to servers when links change, servers that work without Proxier, and no per-user state on the VPS.

## Considered options

- **Per-link credentials** (a UUID per link per server, kept in sync by Proxier): real revocation per link, per-link traffic stats and quotas. It costs a sync job per change and per server, and a server unreachable during a change means drift.
- **Per-subscription credentials**: a middle ground for later.

## Consequences

- Copied connection URIs keep working until rotation. The UI says this wherever a link is disabled or deleted.
- Shared-link alerts can only use fetches (IPs, apps), never actual connections.
- Rotation changes the URIs for everyone on the server. Apps that refresh carry on without noticing.
- Templates are written against a client list (`.Clients`), so moving to per-link credentials later is mostly a sync job plus data.
