# The router-side contract stays compatible with mtvpn

Router sync writes exactly the objects `mtvpn.py` wrote: per domain a DNS static FWD entry and an address-list hostname entry, tagged with the same service tags, in `to_vpn_list`, forwarded to `vpn-doh`. Entries commented `mtvpn:` stay untouchable infra pins, and `telegram-cidr` stays out of bounds. Routers built from today's `fresh-router.rsc` and managed by mtvpn therefore need no migration: the first sync of such a router is a no-op for every tag that already matches. A rename to `proxier:` was considered, deferred, and on 2026-10-09 (planning Phase 4) decided against ([open questions](../open-questions.md)): it would change both the router script and every router at once for no functional gain.

## Consequences

- Proxier inherits mtvpn's quirks deliberately: one `:set` per line, `/import` output scanning, long push timeouts, hostname entries because RouterOS refuses static entries that duplicate dynamic ones. They are documented in the RouterOS integration doc so they don't get "simplified" away.
- The names are per-router settings with mtvpn's defaults, so a router with other names can still be managed.
