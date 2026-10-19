# RouterOS (MikroTik target)

Router sync writes exactly what `mtvpn.py` wrote, so routers set up with `fresh-router.rsc` and managed by mtvpn needed no migration ([ADR 0010](../adr/0010-router-contract-stays-mtvpn-compatible.md)). This document is the contract. The flow is in [router sync](../processes/routing/router-sync.md).

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
- Proxier's ownership rules ([ADR 0013](../adr/0013-one-owner-service-per-domain.md)) mean two tags never claim the same name (names under another tag's suffix are different names, installed under their own tag: [ADR 0016](../adr/0016-every-listed-name-reaches-the-router.md)), so the "remove other entries for these names" part only ever adopts untagged leftovers or moves a name whose owner changed.

**Removal** of a tag:

```
/ip dns static remove [find comment="<tag>" address-list="to_vpn_list"]
/ip firewall address-list remove [find list="to_vpn_list" comment="<tag>" dynamic=no]
```

`<tag>` is escaped (`routeros.Quote`): `"`, `\`, `$` and `?` get a backslash, a control character becomes `\XX` (hex, capitals). A service's tag never needs it; an unmanaged tag being removed is a comment read from the router, which may hold anything, and must not end the string or the line. No real router has had such a comment yet.

## Reading state

```
:foreach i in=[/ip dns static find where address-list="to_vpn_list"] do={:put ([:tostr [/ip dns static get $i comment]] . "|" . [/ip dns static get $i name] . "|" . [:tostr [/ip dns static get $i match-subdomain]] . "|" . [:tostr [/ip dns static get $i type]] . "|" . [:tostr [/ip dns static get $i forward-to]])}
:foreach i in=[/ip firewall address-list find where list="to_vpn_list" dynamic=no] do={:put ([:tostr [/ip firewall address-list get $i comment]] . "|" . [/ip firewall address-list get $i address])}
```

- Lines are trimmed of `\r`, and the fields are split from the **right**: a comment written by hand may hold `|`.
- Tags are read from the DNS static entries, so the address-list-only `telegram-cidr` entries are never mistaken for a service. An empty comment means untagged (DNS and address-list entries are counted). An address-list entry whose comment starts with `mtvpn:` is an infra pin.
- The DNS read prints `type` and `forward-to` too: a tag is unchanged only when every entry is `type=FWD` to the forwarder with the right `match-subdomain`, and its address-list names are the desired ones. A tag whose entries were edited by hand shows as changed.

Test connection and every sync's connect step also run one check line: the version, board and identity (`/system resource`, `/system identity`), the count of the forwarder in `/ip dns forwarders`, of the `mtvpn:doh` pin and of the address list's static entries, each printed as `key|value`.

## Pushing

1. Remove any file a cut-short run left, of this job or another (a cancelled one): `:do {/file remove [find name~"^proxier-sync-"]} on-error={}`. Only one job runs on a router at a time, so no file is in use.
2. Pack the blocks, in push order, into files `proxier-sync-<job>-<n>.rsc` of at most 2 000 domains each. A block that doesn't fit in the current file starts the next one; a block over 2 000 names is split over several files, every part after the first **continued** (no tag-removal lines: only the adoption loops and the adds), so each file stays idempotent. Removal blocks count as no names. Each file starts with a header comment (`# proxier sync · router Home · job #2207 · file 1/2: update openai (+31), update mine (+2)`) and each block with a comment line.
3. Upload each file over SFTP with a plain write: no temporary file, `chmod`, `fsync` or rename, which RouterOS's SFTP server may lack. mtvpn's `scp` spoke SFTP too (OpenSSH 9.0 and later on macOS), so routers accept it. The upload has a minute plus a second per KiB; past that, or on a cancel, Proxier ends it as it ends a command (below).
4. Run `/import file-name=proxier-sync-<job>-<n>.rsc verbose=no` with a timeout of a minute plus 150 ms per name in the file (6 min for a full one), about five times what a hEX S took (64 names in 7.9 s, one in 6 s). A client timeout mid-import leaves a tag half-installed; the block is idempotent, so the retry heals it. RouterOS 7.24.5 once ran a whole import and never ended the session: no exit status, no channel close, and no answer to Proxier's close ([open questions](../open-questions.md#routeros-ending-a-long-import)). So when a command's timeout (or a cancel) has closed the session and the router hasn't confirmed it within 5 s, Proxier closes the whole connection, the jump host's first: a link to the jump host that died without a word blocks every write through it, the router's close included. It then connects again to delete the file and re-read (step 7). An import whose session ends without an exit status costs the connection the same way, and so does an upload whose SFTP session is lost: behind a jump host a broken connection isn't known to be broken yet, and a session opened on it afterwards is never answered. RouterOS 7.24.5 answers an SSH global request (`keepalive@openssh.com`) with `SSH_MSG_UNIMPLEMENTED`, which breaks the connection and every command on it, so Proxier sends a router no keepalive, only its jump host.
5. `/import` can exit with success when a line failed. Its output is scanned for `syntax error|failure|expected|no such item|does not match|bad command|invalid|not enough permissions` (the last one: a user whose group lacks `write` or `ftp`), ignoring SSH noise (warnings, post-quantum notices, blank lines). A non-zero exit fails too.
6. Always delete the file afterwards: `:do {/file remove [find name="<file>"]} on-error={}`.
7. After a file succeeds, each of its blocks is recorded as applied (a split block once its last part landed). After a file fails, the router is read again: `/import` stops at the first failing line, so earlier blocks of the file may have run, and each of its tags that now matches is recorded; the others keep their old applied state.

## Proxier's user on the router

Proxier logs in as its own user in a restricted group. The add page shows the commands. On 7.24.5 they work from an imported router script, and the group is enough for the sync's `/import` (what is untried is in [open questions](../open-questions.md#proxiers-user-from-the-add-page)):

```
/user group add name=proxier policy=read,write,ftp,ssh
/user add name=proxier group=proxier password=[:rndstr length=32 from="abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"]
/user ssh-keys add user=proxier key="<Proxier's public key>"
```

The user gets a random password nobody knows: without `password=`, `/user add` asks for one in the terminal, so the pasted lines stop there, and Winbox and the console would accept an empty one. Proxier only uses the key.

The text lives in one place, `routeros.KeyCommands(user, key)`: the add page shows it, and routing's `RouterRegistrar` hands it to router scripts with every router.

A router script can create the user and add the key itself: a parameter annotated `@fill proxier-ssh-key` is filled with Proxier's public key line (locked, not secret; the generation keeps its fingerprint and says so when the key changed later), and the script's own lines run the commands above with it. Then the router needs no manual step before the awaiting-setup probe connects ([script generation](../processes/router-scripts/script-generation.md)).

Test connection uploads and deletes a one-line `proxier-test-<id>.rsc`, so a group without `ftp` fails the test, not the first sync.

## Names that must match

| Name | Router script | Proxier router field | Default |
|---|---|---|---|
| Address list | `$vpnList` | address list | `to_vpn_list` |
| DoH forwarder | `$dohForwarder` | DoH forwarder | `vpn-doh` |
| Infra pin prefix | `mtvpn:` comments | fixed | `mtvpn:` |
| Telegram CIDR tag | `telegram-cidr` | — (never a service tag) | — |

A service must never have the tag `telegram-cidr`, and no tag may start with `mtvpn:`. Proxier refuses both.
