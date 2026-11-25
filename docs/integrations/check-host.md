# check-host.net (external checker)

The external checker. It tells whether a server's port 443 is reachable over TCP from nodes in Russia and abroad. That separates **down** from **blocked** when Proxier's own checks from home fail ([health](../processes/servers/server-health.md)). It is a public service without an account, so it is used sparingly. The API below is as observed on the real service on 2026-10-08.

## API (as observed)

1. **Nodes**: `GET https://check-host.net/nodes/hosts` (with `Accept: application/json`) answers `{"nodes": {"ru1.node.check-host.net": {"asn": "AS14576", "ip": "185.130.104.238", "location": ["ru", "Russia", "Moscow"]}, …}}`. Proxier refreshes the list daily and keeps node name, country and city. That day there were 3 nodes in Russia (`ru1` to `ru3`) and about 50 elsewhere, none in Finland.
2. **Start a check**: `GET https://check-host.net/check-tcp?host=<ip>:443&node=<node>&node=<node>…` answers `{"ok": 1, "request_id": "4f96e3c5kd18", "permanent_link": "…", "nodes": {"<node>": ["de", "Germany", "Nuremberg", "<ip>", "<asn>"], …}}`.
3. **Results**: `GET https://check-host.net/check-result/<request_id>`, polled every 2 s until every node has answered or 30 s have passed. The answer maps each node to `null` while it works, to `[{"address": "1.1.1.1", "time": 0.019135}]` once connected (`time` in seconds), or to `[{"error": "Connection timed out"}]` / `[{"error": "Connection refused"}]`. Results stay available for the same request id.

Re-checked on 2026-11-25, `/nodes/hosts` listed 60 nodes (`ru1` to `ru3` in Russia, still none in Finland), `check-tcp` and `check-result` matched the response shapes above, and `/about/api` documented the public endpoints without publishing numeric rate limits or quotas.

## Reaching it

From home, check-host.net's own addresses get no answer. On 2026-11-01, `check-host.net` resolved to Cloudflare's `104.21.74.214` and `172.67.163.103`. TCP connected to both, and the TLS handshake hung after the Client Hello whatever the SNI. Cloudflare answered the same request on another of its addresses, `104.16.123.96`, so the block is on the addresses and not on the name.

Each check first connects directly. If a request fails before receiving an HTTP response, for example through a timeout or refused connection, the client opens a route through a proxy server. That request and the rest of the check use the route.

The route uses xray and the first endpoint of the first active, healthy server by ID. The checked server is the last choice because it may be blocked. The job log names the server carrying the request.

An HTTP error or an unreadable answer shows that the direct way works, so the client opens no route. With no healthy server, the check is `unavailable`, and its reason says "no server is healthy to go through". The daily node refresh works the same way, with no server to put last.

## How Proxier uses it

- **Nodes**: 3 in Russia and 3 abroad by default (Settings → Servers, filled on the first daily refresh when empty): the first three Russian nodes by name, and abroad the first node by name of Germany, the Netherlands and Finland, then of Sweden, Poland, France… while fewer than three have one. One request asks all six.
- **When**: every 30 minutes per active server, and on demand when a proxy test fails and the latest external result is older than 10 minutes.
- **Budget**: a scheduled check is refused when the server's last real one is newer than 80 % of the interval, an on-demand one when it is newer than 10 minutes, plus a global cap of 60 checks per rolling hour (settings). A refused check is skipped and stored as one `skipped` row; it never counts toward the next budget.
- **Interpretation**: a node is `ok` if it connected. The verdict uses "at least one node abroad connected" and "how many Russian nodes connected" ([health](../processes/servers/server-health.md#verdict)).
- **Unavailable** (errors, timeouts on the direct way and the route around it, the cap): there is no external data, and verdict reasons say "unconfirmed". An HTTP error, an unreadable answer or no way to the service stores one `unavailable` row.

## Limits of what it tells

- A TCP connection from a Russian node does not prove the proxy works there. DPI and throttling can act after the handshake. The proxy test from home checks whether traffic passes through the endpoint.
- The nodes sit in data centres, not on home or mobile networks, so blocks specific to an ISP or to mobile networks are invisible to them. Probe agents, in the [backlog](../overview.md#backlog), would cover that.
