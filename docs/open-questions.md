# Open questions

Facts that only the real deployment can confirm: real apps, real routers, real networks. Nothing here is a decision still to take. When a question is answered, write the answer into the relevant doc (an ADR if it qualifies) and delete it here. Never fake an answer with a test against a fake.

## Client apps and the stub entry

Which apps replace their list when a refresh returns one stub entry, and which keep the old list on a `404`. [ADR 0004](./adr/0004-one-shared-credential-per-endpoint.md) rests on the first. Seen so far: Shadowrocket and Hiddify replace their list with the stub. Not tried: Happ, v2RayTun, Streisand, v2rayNG, and any app's answer to a `404`.

## check-host.net limits

No page states the rate limits or terms for automated use ([check-host.net](./integrations/check-host.md)). Proxier stays at 6 nodes per check, one check per 30 min per server and 60 checks an hour; a refusal shows as an HTTP error (`unavailable`). No refusal has been seen.

## Russian throttling and the proxy test

Whether the proxy test's 256 KB object and its `stalled` class catch the throttling of foreign hosting networks that stalls after the first 16 to 20 KB ([health](./processes/servers/server-health.md)). Only a real server in a throttled network answers it. Seen once, without the facts to settle it: people found a server very slow while Proxier, testing from home with the default object, called it healthy (so home measured at least 1 Mbit/s). Where they connected from isn't known, so it doesn't tell whether home's own network is spared the throttling (probe agents on other networks, in the backlog, would cover that) or 256 KB is too small to show it.

## Stalled nodes in the router's mihomo

Why new connections from a home router to its servers stall. Seen on one router (RouterOS 7.25, mihomo v1.19.33): some connections to a server's port 443 complete the TCP handshake and then get no answer to the ClientHello, in windows of tens of seconds, and mihomo logs `connect error: context canceled` for the dial. A DoH fetch to `dns.google` through the tunnel then dies, because no ServerHello comes back. The image's defaults check each node every 300 s with a 5 s timeout against a gstatic URL, so the AUTO group stayed on a stalled node for minutes, and the check passed in the gaps between windows. A 20 s interval with a 1.5 s timeout raised the share of DoH fetches that worked from 10 of 36 to 56 of 84, and the rest still failed. The two values are the `HEALTH_CHECK_INTERVAL` and `HEALTH_CHECK_TIMEOUT` envs of the router's mihomo container.

What the tests showed. A plain HTTPS request from the router to the servers never failed (120 of 120), and no mihomo version from 1.19.28 to 1.19.33 behaved clearly differently. From a laptop, on the home network and on a second one, a bare TLS handshake with the Chrome ClientHello (`fp=chrome`, 1.7 KB in mihomo) got through to fi-9, nl-4 and se-3 about one time in five, while the Firefox, iOS and Edge hellos (under 700 bytes), Chrome 120's and Go's own got through every time, in the same seconds. Capping the TCP segment size did not change it, so it is not the path MTU, and forcing Go's hello into three segments did not break it. Separately, a burst of connections from one address (five mihomo instances to one server) was followed by about 10 minutes in which every handshake to that server failed. On the day of the first captures three servers were also down for everyone.

A second router (a hEX, RouterOS 7.25, mihomo v1.19.32, then v1.19.33) showed the same. From 22 minutes after a boot, its log held some 500 DoH errors in 16 minutes (`handshake timed out`, `Connection reset by peer`, `DoH max concurrent queries reached`). For the first 24 minutes after the boot every node showed dead, until the provider was refreshed by hand, and the errors went on for about 14 minutes after the nodes came back. With the DoH pin removed, so that DoH went out the WAN directly, the log held about ten DoH errors in two hours and 200 of 200 test fetches worked. With the pin back, 20 of 20 fetches worked and the forwarder resolved the same names as before. On this router one link had `fp=firefox` and the others `fp=chrome`. For a few minutes a Chrome node was dead while the Firefox node and another Chrome node were alive, but a four-minute watch afterwards gave 64 of 64, 63 of 64 and 64 of 64 checks for the three, so it did not separate the fingerprints.

Not known: what drops the Chrome hello; whether a link with another fingerprint stops the stalls on the router itself (the second router's one `firefox` link did not show it, but its failures could not be made to happen on demand); whether the burst blackouts also happen at a router's own rate; whether the servers' own logs show the dropped sessions; whether the flood after a boot comes back when DoH goes through the tunnel; and what a container does when its subscription has no usable servers (an empty group would send its traffic out the WAN, but that was not tried).

## The tailnet key's expiry

The node joins headscale 0.29 with a pre-auth key and survives restarts on its saved state. What happens when the node key expires is not seen: whether Settings → Integrations → Tailnet's new pre-auth key brings it back as expected ([platform](./modules/platform.md#tailnet)).

## A reset router's first fetch

After `/system reset-configuration no-defaults=yes` a router has no internet, so `/tool fetch` needs a DHCP client on the WAN port first, and the script then adds its own (`/ip dhcp-client add interface=$wanIface`). RouterOS may refuse a second client on the port and stop the import. Seen: a file uploaded by hand imports whole on 7.24.5, the script's own DHCP client comes up, and the router connects and syncs 2 to 10 minutes later. Not tried: `/tool fetch` itself (its user agent, the "was fetched" notification, the `404` of a second fetch) and a hand-added DHCP client beside the script's. Until then the generation page tells the member to give the WAN port a DHCP client before fetching ([script generation](./processes/router-scripts/script-generation.md)).

## Proxier's user from the add page

The key commands work from an imported router script on 7.24.5, and the `proxier` group is enough for the sync's `/import` ([RouterOS](./integrations/routeros.md#proxiers-user-on-the-router)). Not tried: the same lines pasted from the add page (expected to behave alike), and `/ip dns forwarders` in the connection check.

## A subdomain beside its parent suffix

A name under another service's suffix (`console.anthropic.com` in `mine`, `anthropic.com` in `anthropic`) is installed as its own FWD entry and address-list entry ([ADR 0016](./adr/0016-every-listed-name-reaches-the-router.md)). Expected, as with mtvpn: a query for the subdomain, matched by both static entries, goes to the same forwarder and fills the same address list. Not seen on a real router: which static entry RouterOS matches first, and whether the subdomain's addresses reach `to_vpn_list` before any client asks for it.

## Base bootstrap on a real VPS

Base bootstrap turns TCP BBR on and resets ufw ([provisioning](./processes/servers/server-provisioning.md#steps-the-job)). Seen in an Ubuntu 24.04 container with systemd: ufw's reset keeps an open session. Not seen: BBR on a real VPS's kernel (Docker Desktop's has no `tcp_bbr`, so only the warning was seen). The first real provisioning answers it.

## RouterOS ending a long `/import`

RouterOS 7.24.5 answers an SSH global request (`keepalive@openssh.com`) with `SSH_MSG_UNIMPLEMENTED`, which ends Go's SSH packet loop ("ssh: unexpected message type 3"). Every command still running at the first keepalive, 30 s in, ended without an exit status, direct or behind a jump host, and once reading a router took over 30 s, every sync failed. Proxier sends routers no keepalive, only their jump hosts ([router sync](./processes/routing/router-sync.md#rules)). Since then, reads of 30–33 s on RB5009s have ended with their exit status. Not seen yet: an `/import` past 30 s ending with its.

## A hEX S resolving 3,226 names

Once its routing list reached 3,226 names, Telepat's (hEX S, 7.24.5) every read of its DNS entries outlasted 10 min, and its connect, which counts `/ip dns forwarders`, took 18.5 s instead of 2.7 s. Two RB5009s with the same names read in 30–33 s. A CHR whose dns process was saturated (resolving the same names through an unreachable forwarder) answered nothing under `/ip dns` for 10 min, so Telepat's dns process is likely saturated too ([reading state](./integrations/routeros.md#reading-state)). Its import of microsoft's 678 names didn't end within 2m41.7s, neither with each address-list entry right after its DNS entry nor with them last, while its read took about 9 min; the RB5009s imported it in 36–40 s. The import now has twice the read's time on top ([router sync](./processes/routing/router-sync.md#rules)). Not seen: what saturates it, whether it's the slow forwarder through the VPN, the CPU, or a full cache (`cache-used` at `cache-size`, 16 MiB on Telepat and full) evicting names that are then resolved again; and whether its import of 678 names ends within about 20 min.

## Passkeys and the authenticator app on real devices

Checked with a software passkey in tests, Chrome's virtual authenticator and Bitwarden ([sign-in](./processes/platform/sign-in.md#steps-passkeys)). Not seen: adding and using a passkey in Safari on macOS and iOS (iCloud Keychain), in 1Password, on Android, and with a hardware key; whether the username field's autofill offers the passkey in each; whether a synced passkey's signature counter stays 0 (a counter that goes back is refused). Not seen either: a real authenticator app scanning the QR code and its codes being accepted, and the phone's clock drift staying within one 30 s step.

## The browser page's apps

None of the [browser page](./integrations/subscription-format.md#browser-page)'s deep links and none of its manual steps were tried on a real device: whether each **Add to {app}** button opens its app and adds the link under the subscription's title, and whether the loose words of each manual import ("tap + and choose to import from the clipboard") match what the app shows. The Shadowrocket and Happ link syntax comes from other panels' code, not from the apps' makers. Which apps are in the Russian App Store changes month to month: re-check with `https://itunes.apple.com/lookup?id=<id>&country=ru` (an empty `results` means not there) and update the notes in `linkpage/apps.yaml`. Not seen either: the page on a real phone (tabs, Copy link over https, the QR code scanned by another phone) and the tab an iPad opens.

## Not yet seen on real devices

- Most pages were checked over HTTP tests or in a headless browser, not on a phone: the forms (new server, generate), dialogs, live logs and charts.
- Telegram delivery of every notification text.
- Several members on one real bot: **Detect chat** for each member's own chat (and a group chat), and each one's messages reaching only their chat.
- The bot on a real phone: `/link` from the command menu, the subscription buttons, the URL copied with a tap from the `<code>` block, and long polling from home holding up for days (through the router's tunnel too, if `api.telegram.org` goes there).
- The IP country lookup (ipinfo.io) from the deployment.
- An agent session (`/agent/v1`) driven by a real Claude Code or Codex run.
