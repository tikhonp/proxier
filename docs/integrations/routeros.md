# RouterOS (MikroTik target)

Router sync writes exactly what `mtvpn.py` wrote, so routers set up with `fresh-router.rsc` and managed by mtvpn need no migration ([ADR 0010](../adr/0010-router-contract-stays-mtvpn-compatible.md)). This document is the contract. The flow is in [router sync](../processes/routing/router-sync.md).

## Split of responsibilities

| Owned by the router script (`fresh-router.rsc`) | Owned by Proxier |
|---|---|
| Bridges, addressing, DHCP, firewall, NAT | — |
| The mangle marks (`mtvpn:conn-*`, `mtvpn:route-*`, `mtvpn:mss-clamp`), the routing table `to_vpn_table`, and its fail-open default route `mtvpn:route` | — |
| The DoH forwarder `vpn-doh` and the static A record that bootstraps it | — |
| The mihomo container (with `SUB1` = a subscription link), veth, netwatch watchdogs | — |
| Infra pins in the address list: `dns.google` (`mtvpn:doh`) and `core.telegram.org` (`mtvpn:tg-fetch`) | — |
| The Telegram CIDR scheduler (`telegram-cidr` entries, address-list only) | — |
| — | The **contents** of the address list `to_vpn_list` and the matching `/ip dns static` FWD entries, per service tag |

Requirements: RouterOS 7 (the router script targets 7.24.5+), SSH with key login for Proxier's user, and LAN clients using the router as their only DNS server. Otherwise subdomain coverage silently degrades: with Tailscale clients, that means `--accept-dns=false` and the router in the host's resolver.

## Objects per domain

For each domain of a tag, two objects, both with `comment=<tag>`:

```
/ip dns static add name=<domain> type=FWD match-subdomain=<yes|no> forward-to=vpn-doh address-list=to_vpn_list comment="<tag>"
/ip firewall address-list add list=to_vpn_list address=<domain> comment="<tag>"
```

- `match-subdomain=yes` for suffix domains, `no` for exact ones.
- The FWD entry makes the name resolve through the tunnel's DoH forwarder, and puts every resolved address into the address list (that is how subdomains get routed).
- The address-list entry is a hostname entry for the name itself. RouterOS refuses a static entry that duplicates a dynamic one in the same list, so the add is wrapped in `:do {…} on-error={}`.

## Service block

One block per tag, run with `/import`. It is idempotent: running it twice leaves the same state.

```
{
/ip dns static remove [find comment="<tag>" address-list="to_vpn_list"]
/ip firewall address-list remove [find list="to_vpn_list" comment="<tag>" dynamic=no]
:local ours [:toarray ""]
:set ($ours->"<domain1>") 1
:set ($ours->"<domain2>") 1
…
:foreach e in=[/ip dns static find where address-list="to_vpn_list" type=FWD] do={:if ([:typeof ($ours->[/ip dns static get $e name])]!="nothing") do={/ip dns static remove $e}}
:foreach e in=[/ip firewall address-list find where list="to_vpn_list" dynamic=no] do={:if ([:typeof ($ours->[/ip firewall address-list get $e address])]!="nothing" && [:pick [:tostr [/ip firewall address-list get $e comment]] 0 6]!="mtvpn:") do={/ip firewall address-list remove $e}}
/ip dns static add name=<domain1> type=FWD match-subdomain=yes forward-to=vpn-doh address-list=to_vpn_list comment="<tag>"
:do {/ip firewall address-list add list=to_vpn_list address=<domain1> comment="<tag>"} on-error={}
…
}
```

- It removes the tag's own entries, then any other entry for the same names (adopting untagged duplicates), but never an `mtvpn:` entry. Then it adds everything, tagged.
- One `:set` per line: the console caps line length, so one big array literal fails.
- The keyed array makes adoption linear instead of a `[find]` per domain.
- Proxier's ownership rules ([ADR 0013](../adr/0013-one-owner-service-per-domain.md)) mean two tags never claim the same name, so the "remove other entries for these names" part only ever adopts untagged leftovers or moves a name whose owner changed.

**Removal** of a tag:

```
/ip dns static remove [find comment="<tag>" address-list="to_vpn_list"]
/ip firewall address-list remove [find list="to_vpn_list" comment="<tag>" dynamic=no]
```

## Reading state

```
:foreach i in=[/ip dns static find where address-list="to_vpn_list"] do={:put ([:tostr [/ip dns static get $i comment]] . "|" . [/ip dns static get $i name] . "|" . [:tostr [/ip dns static get $i match-subdomain]])}
:foreach i in=[/ip firewall address-list find where list="to_vpn_list" dynamic=no] do={:put ([:tostr [/ip firewall address-list get $i comment]] . "|" . [/ip firewall address-list get $i address])}
```

Tags are read from the DNS static entries, so the address-list-only `telegram-cidr` entries are never mistaken for a service. An empty comment means untagged.

## Pushing

1. Upload the script over SFTP (or SCP, if the router doesn't offer SFTP — to verify, [open questions](../open-questions.md)) as `proxier-sync-<job>-<n>.rsc`, at most 2,000 domains per file.
2. Run `/import file-name=proxier-sync-<job>-<n>.rsc verbose=no` with a 30-minute timeout. A client timeout mid-import would leave a tag half-installed. The block is idempotent, so a retry heals it, but a long timeout avoids the problem.
3. `/import` can exit with success when a line failed. Its output is scanned for `syntax error|failure|expected|no such item|does not match|bad command|invalid`, ignoring SSH noise (warnings, post-quantum notices, blank lines).
4. Always delete the file afterwards: `:do {/file remove [find name="<file>"]} on-error={}`.

## Names that must match

| Name | Router script | Proxier router field | Default |
|---|---|---|---|
| Address list | `$vpnList` | address list | `to_vpn_list` |
| DoH forwarder | `$dohForwarder` | DoH forwarder | `vpn-doh` |
| Infra pin prefix | `mtvpn:` comments | fixed | `mtvpn:` |
| Telegram CIDR tag | `telegram-cidr` | — (never a service tag) | — |

A service must never have the tag `telegram-cidr`, and no tag may start with `mtvpn:`. Proxier refuses both.
