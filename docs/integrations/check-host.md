# check-host.net (external checker)

The first external checker. It tells whether a server's port 443 is reachable over TCP from nodes in Russia and abroad. That separates **down** from **blocked** when Proxier's own checks from home fail ([health](../processes/servers/server-health.md)). It is a public service without an account, so it is used sparingly. The API below was verified against the real service on 2026-10-08.

## API (as observed)

1. **Nodes**: `GET https://check-host.net/nodes/hosts` (with `Accept: application/json`) answers `{"nodes": {"ru1.node.check-host.net": {"asn": "AS14576", "ip": "185.130.104.238", "location": ["ru", "Russia", "Moscow"]}, …}}`. Proxier refreshes the list daily and keeps node name, country and city. At the time of writing there were 3 nodes in Russia (`ru1`–`ru3`) and about 50 elsewhere, none in Finland.
2. **Start a check**: `GET https://check-host.net/check-tcp?host=<ip>:443&node=<node>&node=<node>…` answers `{"ok": 1, "request_id": "4f96e3c5kd18", "permanent_link": "…", "nodes": {"<node>": ["de", "Germany", "Nuremberg", "<ip>", "<asn>"], …}}`.
3. **Results**: `GET https://check-host.net/check-result/<request_id>`, polled every 2 s until every node has answered or 30 s have passed. The answer maps each node to `null` while it works, to `[{"address": "1.1.1.1", "time": 0.019135}]` once connected (`time` in seconds), or to `[{"error": "Connection timed out"}]` / `[{"error": "Connection refused"}]`. Results stay available for the same request id.

## How Proxier uses it

- **Nodes**: 3 in Russia and 3 abroad by default (Settings → Servers, filled on the first daily refresh when empty): the first three Russian nodes by name, and abroad the first node by name of Germany, the Netherlands and Finland, then of Sweden, Poland, France… while fewer than three have one. One request asks all six.
- **When**: every 30 minutes per active server, and on demand when a proxy test fails and the latest external result is older than 10 minutes.
- **Budget**: a scheduled check is refused when the server's last real one is newer than 80 % of the interval, an on-demand one when it is newer than 10 minutes, plus a global cap of 60 checks per rolling hour (settings). A refused check is skipped and stored as one `skipped` row; it never counts toward the next budget.
- **Interpretation**: a node is `ok` if it connected. The verdict uses "at least one node abroad connected" and "how many Russian nodes connected" ([health](../processes/servers/server-health.md#verdict)).
- **Unavailable** (errors, timeouts, the cap): there is no external data, and verdict reasons say "unconfirmed". An HTTP error or unreadable answer stores one `unavailable` row.

## Limits of what it tells

- A TCP connect from a Russian node doesn't prove the proxy works from Russia. DPI and throttling act after the handshake, which is why the proxy test from home is the ground truth.
- The nodes sit in data centres, not on home or mobile networks, so blocks specific to an ISP or to mobile networks are invisible to them. Probe agents, in the backlog, would cover that.
