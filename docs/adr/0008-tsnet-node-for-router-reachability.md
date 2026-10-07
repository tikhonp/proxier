# Proxier joins the tailnet as its own node to reach routers

Routers sit on LANs that Proxier reaches through jump hosts on the headscale tailnet (mtvpn's `-r JUMPHOST:HOST`). Proxier embeds a tailnet client (`tsnet`) and joins headscale as its own node, `proxier`, using it only for outgoing SSH to jump hosts and routers. This keeps the container off blackberry's host network, gives Proxier its own identity in headscale ACLs, and makes the dependency explicit in Proxier's configuration rather than in how the host happens to be set up.

## Considered options

- **Host networking** (`network_mode: host`) to use blackberry's own Tailscale: no extra node, but the container sees the whole host network and depends on the host's tailnet state.
- **A Tailscale sidecar container**: works, but it is another container with its own state to run beside the binary.

## Consequences

- The node needs a pre-auth key on first start, and its key expiry must be handled (a non-expiring node, or re-authentication from Settings).
- Routers that are reachable directly (blackberry's own LAN) don't need the tailnet at all.
