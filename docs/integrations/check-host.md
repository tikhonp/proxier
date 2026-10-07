# check-host.net (external checker)

The first external checker. It tells whether a server's port 443 is reachable over TCP from nodes in Russia and abroad. That separates **down** from **blocked** when Proxier's own checks from home fail ([health](../processes/servers/server-health.md)). It is a public service without an account, so it is used sparingly. The exact API must be verified during the build ([open questions](../open-questions.md)).

## API (as expected)

1. **Nodes**: `GET https://check-host.net/nodes/hosts` (with `Accept: application/json`) lists nodes with their country. Proxier refreshes the list daily and offers the nodes in Settings, grouped as "Russia" and "abroad".
2. **Start a check**: `GET https://check-host.net/check-tcp?host=<ip>:443&node=<node>&node=<node>…` returns a `request_id` and the nodes it was given to.
3. **Results**: `GET https://check-host.net/check-result/<request_id>`, polled every 2 s until every node has answered or 30 s have passed. A node's answer is a connect time, or an error ("Connection timed out", "Connection refused", …).

## How Proxier uses it

- **Nodes**: 3 in Russia and 3 abroad by default (Settings), e.g. Moscow, Saint Petersburg, Yekaterinburg; Germany, the Netherlands, Finland. One request asks all six.
- **When**: every 30 minutes per active server, and on demand when a proxy test fails and the latest external result is older than 10 minutes.
- **Budget**: at most one scheduled and one on-demand check per server in their windows, plus a global cap of 60 checks per hour (setting). A check that would exceed the cap is skipped and recorded as skipped.
- **Interpretation**: a node is `ok` if it connected. The verdict uses "at least one node abroad connected" and "how many Russian nodes connected" ([health](../processes/servers/server-health.md#verdict)).
- **Unavailable** (errors, timeouts, the cap): there is no external data, and verdict reasons say "unconfirmed".

## Limits of what it tells

- A TCP connect from a Russian node doesn't prove the proxy works from Russia. DPI and throttling act after the handshake, which is why the proxy test from home is the ground truth.
- The nodes sit in data centres, not on home or mobile networks, so blocks specific to an ISP or to mobile networks are invisible to them. Probe agents, in the backlog, would cover that.
