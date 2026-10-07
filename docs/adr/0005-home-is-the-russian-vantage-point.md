# Proxier runs at home, which makes it its own Russian vantage point

Proxier runs on blackberry, at home in Russia, behind the sh-main gateway. Its outbound connections therefore see the Russian network: the proxy test from Proxier is the ground truth for "usable from Russia", including throttling that only shows after the first 16–20 KB of a connection. check-host.net nodes abroad, together with the SSH self-check, separate **blocked** (fine on the server, reachable abroad, unusable from Russia) from **down**. A reference check of home's own internet freezes verdicts when home is offline or cut off from foreign sites, so that a home problem never looks like a fleet problem.

## Considered options

- **A VPS abroad**: always reachable, independent of home internet, but blind to Russian blocking without a separate in-Russia probe.
- **A VPS in Russia**: sees blocks and stays up when home is down, but costs extra and is another host holding every secret.

## Consequences

- Traffic from Proxier to its servers must never enter the home router's VPN. Routing lists refuse to cover a server's hostname.
- When home is offline, the admin UI and every link URL are unreachable. Apps keep their last list, so connections are unaffected.
- Blocks specific to mobile networks or other ISPs stay invisible until probe agents exist.
