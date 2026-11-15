# Open questions

These integration checks need real devices, accounts or networks. Record each result in the relevant integration or process document.

## Client subscription refresh

Check whether each supported app replaces its server list when a refresh returns a stub entry, and what it keeps after a `404`. See [subscription format](./integrations/subscription-format.md#stub-entry) and [ADR 0004](./adr/0004-one-shared-credential-per-endpoint.md).

## External checks and throttling

Confirm check-host.net's limits for automated use. Proxier currently caps requests at 6 nodes per check, one check per 30 minutes per server and 60 checks per hour.

Verify whether the proxy test's 256 KB object and `stalled` result detect throttling on the networks Proxier monitors. A result from one network does not establish reachability from another. See [server health](./processes/servers/server-health.md).

Check how a router's proxy container handles a subscription with no usable servers and whether bursts of handshakes trigger temporary connection failures.

## Tailnet key expiry

Verify that replacing the pre-auth key in Settings restores connectivity after a node key expires, for each supported control server. See [tailnet](./modules/platform.md#tailnet).

## Router bootstrap and sync

- On a reset router, verify the first script fetch, its one-use URL, the fetch notification and the interaction between a temporary WAN DHCP client and the script's own client. See [script generation](./processes/router-scripts/script-generation.md).
- Verify the SSH user commands copied from the add-router page and the DNS forwarder connection check. See [RouterOS](./integrations/routeros.md#proxiers-user-on-the-router).
- Check RouterOS behavior when an exact subdomain and its parent suffix belong to separate services, including when their addresses enter the routing list. See [ADR 0016](./adr/0016-every-listed-name-reaches-the-router.md).

Check long RouterOS imports, including sessions that finish without an exit status or never close. Verify timeout, reconnect and cleanup behavior behind a jump host, and confirm that retries detect blocks already applied.

## Server lifecycle

Verify bootstrap, credential rotation and IP change on supported VPS images and providers. For an added IP, confirm whether the provider configures the interface automatically, whether SSH and the proxy stack answer on both addresses, and whether the new IP works from the monitored network before DNS changes. See [IP change](./processes/servers/ip-change.md).

## Browsers and authentication

Verify passkeys with Safari, iCloud Keychain, Android, password managers and hardware keys. Check username autofill and synced passkey counters. Verify authenticator QR enrollment and clock tolerance on real phones. See [sign-in](./processes/platform/sign-in.md).

Check mobile forms, dialogs, logs and charts on real iOS and Android devices. For each client app, verify the subscription page's import button, clipboard instructions and QR code. App availability varies by store region. See [subscription format](./integrations/subscription-format.md#browser-page).

## Notifications and agent sessions

Verify Telegram delivery to private and group chats, isolation between members, long polling over extended periods and the bot's subscription commands. Check IP country lookups from the deployment network.

Exercise template and router-script agent sessions with supported clients, including stale ETags, previews and token rejection after a session ends. See [template authoring](./processes/servers/template-authoring.md).

## AI IP reputation

Confirm Net.Coffee automated-use permission, quotas and attribution requirements. Its anonymous API works, but no integration contract has been found. Compare results for ordinary IPv4 and IPv6 proxy exits against its pages. Verify how supported client apps preserve the selected endpoint when refreshed display names gain a different score. No Codex API exit-IP detection method is verified. See [Net.Coffee](./integrations/net-coffee.md).
