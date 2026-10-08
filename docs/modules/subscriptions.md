# Subscriptions

The subscriptions module gives people and devices access to servers. A **subscription** is a named, ordered set of servers. A **link** is a named secret URL for one person or device that serves the connection URIs of exactly one subscription. Client apps (Happ, v2RayTun, Hiddify, Shadowrocket, Streisand, v2rayNG…) add the link's URL as a subscription and refresh it. That's all the link holder ever sees.

The module uses the servers module through its `EndpointCatalog` port and never touches servers itself.

## Subscriptions

| Field | Meaning | Default |
|---|---|---|
| Name | Admin-facing, unique (exact match), at most 60 characters. | — |
| Title | What apps show as the subscription's name (`profile-title`), at most 60 characters. | the name |
| Description | Admin-facing note. | — |
| Servers | Ordered list of active servers. Every endpoint of each server is served, in that order. | empty |
| Formats | Which formats links may be served in, and the default. First version: `uri-plain`, `uri-base64` ([subscription format](../integrations/subscription-format.md)). | both allowed, `uri-plain` default |
| Update interval | Whole hours suggested to apps between refreshes (`profile-update-interval`), 1–168. | 12 |
| Hide unhealthy servers | Off, or on with the states to hide, chosen from `blocked`, `down`, `degraded`, `unknown` (default `blocked`, `down`; `paused` never hides), and a grace period of 0–10 080 min (default 30). | off |
| Add new servers automatically | Every server that becomes active is appended to this subscription. | off |

Rules:

- A server can be in any number of subscriptions. Only **active** servers can be added. A server that is retired is removed from every subscription by the module's reaction to `server.retired`.
- **Hide unhealthy**: a server whose health state has been one of the hidden states for at least the grace period is left out of the output, and comes back as soon as it leaves those states. If hiding would leave the output with no servers, nothing is hidden and `subscription.all_unhealthy` is raised. A subscription never serves an empty list because of health ([fetch](../processes/subscriptions/subscription-fetch.md)).
- A server's display name in the output is its location as the admin entered it ("🇳🇱 Netherlands 1"), whatever the link's language: a link's language changes only its stub entries.
- A member the servers module no longer serves (retiring, or without endpoints) stays in the list, shown "not in service", and is never served; its retirement removes it.
- A subscription with links can't be deleted. Its links must be moved to another subscription or deleted first. A deleted link holds its subscription until its tombstone ends.

Flows: [subscription management](../processes/subscriptions/subscription-management.md).

## Links

| Field | Meaning | Default |
|---|---|---|
| Name | Who or what it is for: "Mom — iPhone", "Router — Parents". Unique. | — |
| Subscription | Exactly one. | the only one, if there is one |
| Token | ≥ 128 random bits, URL-safe. The URL is `https://proxier.tikhonnnnn.com/s/{token}`. | generated |
| State | `active`, `disabled` or `deleted`. "Expired" is an active link past its expiry. | active |
| Expiry | Optional date and time; after it the link serves its stub entry. | none |
| Language | Language of its stub entry text. | Settings default |
| Format override | One of the subscription's allowed formats, for an app that needs it. | none |
| Alert thresholds | Override the shared-link alert thresholds, or mute alerts for this link. | Settings defaults |
| Note | Admin-facing. | — |

### What a link serves

| Link | Output |
|---|---|
| active, not expired | The subscription's connection URIs, in order, minus hidden servers. |
| active, but the subscription has no servers | One stub entry: "⚠️ No servers yet". |
| active, past expiry | One stub entry: "⏳ Expired on 1 Dec 2026 · contact @tikhonp" (the last day it worked, in the link's language). |
| disabled | One stub entry: "⛔ Link disabled · contact @tikhonp". |
| deleted, within the tombstone period (`subscriptions.tombstone`, 30 days) | One stub entry: "⛔ Link removed". |
| deleted, after the tombstone period, or a token that never existed | `404`. |

Headers, formats and rate limits: [fetch](../processes/subscriptions/subscription-fetch.md).

### Disabling is not revoking

Credentials are **shared per endpoint** ([ADR 0004](../adr/0004-one-shared-credential-per-endpoint.md)): every link to a server serves the same credential. So:

- **Disable, expire, delete** stop the link's *updates*. On its next refresh, the app replaces its server list with the stub entry. Most apps then have nothing left to connect with. That is why a disabled link serves a stub entry rather than an error: on an error, apps keep their old list.
- A person who copied a connection URI out of the app, or whose app stopped refreshing, **keeps access** until the server's credentials are rotated.
- **Cut-off** is the real revoke: it disables the link and rotates every server in its subscription. Every other link then receives the new URIs on its next refresh. Anyone holding only old URIs (the cut-off link, copied URIs) loses access.

The UI says this wherever it matters: the disable dialog, the delete dialog, and the cut-off dialog, which lists the servers to be rotated and how many other links will get new URIs.

### Actions

| Action | Effect |
|---|---|
| Copy URL / Show QR | Copy URL is one tap: the link page carries the URL in the button (on screen it stays masked until **Reveal**). The QR code encodes the URL, for scanning with the phone's app. |
| Disable / Enable | Switches between the stub entry and the real output. |
| Regenerate token | The old URL returns `404` at once, and a new URL is shown. Apps holding the old URL keep their last list, so use it when the URL leaked but the holder should keep access. |
| Change subscription | The next fetch serves the other subscription. |
| Set / extend / clear expiry | — |
| Cut off | Disable, plus rotate every server in the subscription ([rotation](../processes/servers/credential-rotation.md)). |
| Delete | Removed from lists. The URL serves "⛔ Link removed" for the tombstone period, then `404`. |

Flows: [link lifecycle](../processes/subscriptions/link-lifecycle.md).

## Fetch log and shared-link alerts

Every fetch of a link is recorded: time, IP, network (IPv4 /24, IPv6 /48), user agent, the app detected from it, the format, and the outcome. The link page shows the log, distinct networks and apps over 24 h and 7 d, and the last fetch.

A link fetched from more networks or apps than one person's devices explain raises a **shared-link alert** (defaults: more than 4 networks or more than 3 apps in 24 hours, at most one alert per link per day): [shared-link alerts](../processes/subscriptions/shared-link-alerts.md).

## Pages

- **Subscriptions**: name, title, number of servers (with health dots), number of links, formats, hide-unhealthy on/off.
- **Subscription**: the server list (drag to reorder; add from active servers; each row shows health and whether it is hidden right now), settings, a **preview** of exactly what a link gets right now, the links of this subscription, and activity.
- **Links**: every link. Columns: name, subscription, state (active / disabled / expired / deleted), expiry, last fetch with app icon, fetches in 24 h, alert badge. Filters: subscription, state, expiring within 7 days, with alerts.
- **Link**: URL (copy, QR, regenerate), state and expiry, subscription, the preview of its current output, the fetch log, alerts, actions, activity.
- **New link**: a small dialog (name, subscription, optional expiry, language). It ends on the link page with the URL and QR ready to share.

## Settings

Section `subscriptions` (its page arrives with the expiry and alert settings):

| Key | Default | Meaning |
|---|---|---|
| `subscriptions.link_language` | `ru` | Language of new links' stub entries. |
| `subscriptions.expiry_warning` | 72 h | `link.expiring_soon` this long before an expiry (1–720 h). |
| `subscriptions.alert_networks` | 4 | Shared-link alert above this many networks in 24 h. |
| `subscriptions.alert_apps` | 3 | The same for apps. |
| `subscriptions.network_country_url` | `https://ipinfo.io/{ip}/country` | Country lookup of a network's first address; empty turns it off. |
| `subscriptions.tombstone` | 30 days | How long a deleted link serves "⛔ Link removed" (and holds its subscription) before it answers `404` (1–365 days). |
| `subscriptions.fetch_retention` | 90 days | Fetches older than this are deleted (at least 7 days). |

## Ports for other modules

| Port | Used by | Contract |
|---|---|---|
| `LinkIssuer` (Phase 4: its consumer decides its shape) | router scripts | Create a link with a given name in a given subscription, and return its URL. Used for a new router's mihomo subscription. |

## Events

`subscription.*`, `link.*`: see [events](../events.md#subscriptions).
