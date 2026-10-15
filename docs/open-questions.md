# Open questions

Facts that only the real deployment can confirm: real apps, real routers, real networks. Nothing here is a decision still to take. When a question is answered, write the answer into the relevant doc (an ADR if it qualifies) and delete it here. Never fake an answer with a test against a fake.

## Client apps and the stub entry

Which apps replace their list when a refresh returns one stub entry, and which keep the old list on a `404`. [ADR 0004](./adr/0004-one-shared-credential-per-endpoint.md) rests on the first. Seen so far: Shadowrocket and Hiddify replace their list with the stub. Not tried: Happ, v2RayTun, Streisand, v2rayNG, and any app's answer to a `404`.

## check-host.net limits

No page states the rate limits or terms for automated use ([check-host.net](./integrations/check-host.md)). Proxier stays at 6 nodes per check, one check per 30 min per server and 60 checks an hour; a refusal shows as an HTTP error (`unavailable`). No refusal has been seen.

## Russian throttling and the proxy test

Whether the proxy test's 256 KB object and its `stalled` class catch the throttling of foreign hosting networks that stalls after the first 16 to 20 KB ([health](./processes/servers/server-health.md)). Only a real server in a throttled network answers it. Not seen yet.

## The tailnet key's expiry

The node joins headscale 0.29 with a pre-auth key and survives restarts on its saved state. What happens when the node key expires is not seen: whether Settings → Integrations → Tailnet's new pre-auth key brings it back as expected ([platform](./modules/platform.md#tailnet)).

## A reset router's first fetch

After `/system reset-configuration no-defaults=yes` a router has no internet, so `/tool fetch` needs a DHCP client on the WAN port first, and the script then adds its own (`/ip dhcp-client add interface=$wanIface`). RouterOS may refuse a second client on the port and stop the import. Seen: a file uploaded by hand imports whole on 7.24.5, the script's own DHCP client comes up, and the router connects and syncs 2 to 10 minutes later. Not tried: `/tool fetch` itself (its user agent, the "was fetched" notification, the `404` of a second fetch) and a hand-added DHCP client beside the script's. Until then the generation page tells the admin to give the WAN port a DHCP client before fetching ([script generation](./processes/router-scripts/script-generation.md)).

## Proxier's user from the add page

The key commands work from an imported router script on 7.24.5, and the `proxier` group is enough for the sync's `/import` ([RouterOS](./integrations/routeros.md#proxiers-user-on-the-router)). Not tried: the same lines pasted from the add page (expected to behave alike), and `/ip dns forwarders` in the connection check.

## A subdomain beside its parent suffix

A name under another service's suffix (`console.anthropic.com` in `mine`, `anthropic.com` in `anthropic`) is installed as its own FWD entry and address-list entry ([ADR 0016](./adr/0016-every-listed-name-reaches-the-router.md)). Expected, as with mtvpn: a query for the subdomain, matched by both static entries, goes to the same forwarder and fills the same address list. Not seen on a real router: which static entry RouterOS matches first, and whether the subdomain's addresses reach `to_vpn_list` before any client asks for it.

## RouterOS ending a long `/import`

One router (hEX S, 7.24.5, behind a jump host) ran a 370 KiB import in full and never sent the exit status or closed the channel. Proxier now ends such a command at its timeout ([router sync](./processes/routing/router-sync.md#rules)). Not known: whether RouterOS ever ends the session after a long `/import`, and whether its `inactivity-timeout` or the jump host plays a part. Another router's first sync through a jump host ended its `/import` promptly, so not every long import hangs. `ssh -v proxier@<router> '/import file-name=<big file> verbose=no'` through the jump host would answer it. If RouterOS never ends the session, each file takes the full 30-minute timeout, and an import timeout sized to the file would be the fix.

## Passkeys and the authenticator app on real devices

Checked with a software passkey in tests and Chrome's virtual authenticator, not with a real one. Not seen: adding and using a passkey in Safari on macOS and iOS (iCloud Keychain), in a password manager (1Password, Bitwarden), on Android, and with a hardware key; whether the username field's autofill offers the passkey in each; whether a synced passkey's signature counter stays 0 (a counter that goes back is refused). Not seen either: a real authenticator app scanning the QR code and its codes being accepted, and the phone's clock drift staying within one 30 s step.

## Not yet seen on real devices

- Most pages were checked over HTTP tests or in a headless browser, not on a phone: the forms (new server, generate), dialogs, live logs and charts.
- Telegram delivery of every notification text.
- Several members on one real bot: **Detect chat** for each member's own chat (and a group chat), and each one's messages reaching only their chat.
- The IP country lookup (ipinfo.io) from the deployment.
- An agent session (`/agent/v1`) driven by a real Claude Code or Codex run.
