# Servers are managed agentlessly over SSH, with host keys pinned on first contact

Proxier installs nothing of its own on servers. Provisioning, redeploys, self-checks and stats all go over SSH with Proxier's own key. A server is just Debian or Ubuntu with Docker and the stack, and it keeps working if Proxier disappears. Each host key (server, jump host, router) is pinned on first contact. A changed key stops every job with that host and notifies the admin until the admin accepts the new key, because silently accepting it would hand a man in the middle the means to read and change the server's secrets.

## Considered options

- **An agent on each server** (pushing metrics, applying configs): real-time data and no inbound SSH, but another daemon to build, update and secure on every VPS, and another fingerprint for censors to detect.

## Consequences

- Stats are 5-minute samples read over SSH, not real-time.
- Rebuilding a VPS at the same address (a provider reinstall) shows a host-key change that the admin must accept.
