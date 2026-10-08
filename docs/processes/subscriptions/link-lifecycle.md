# Link lifecycle

**Actors**: Admin; the link holder's app (only through [fetch](./subscription-fetch.md)); the expiry scan (queue `maintenance`); the notifier.

A link is what you hand to one person or device: a name and a secret URL serving one subscription. Credentials are shared per endpoint ([ADR 0004](../../adr/0004-one-shared-credential-per-endpoint.md)), so a link's states control what its URL **serves**, not what an already-imported connection URI can do. Real revocation is the cut-off, which rotates servers.

## Steps — creating and sharing

1. Links → **New link** (or **New link** on a subscription page). Fields:
   - **name** ("Mom — iPhone");
   - **subscription**;
   - optional **expiry** (a date, meaning the end of that day in the display time zone, or a date and time);
   - **language** of the stub texts;
   - **note**.
2. Proxier generates the token and creates the link as **active**. The link page opens with the URL, **Copy URL** and **Show QR**, under the band "Link created. Share the URL or let them scan the code." On screen the URL stays masked until **Reveal**; the Copy button holds it, so copying is one tap. → `link.created{subscription, expiry}`
3. The admin sends the URL, or shows the QR to be scanned in the app.

## Steps — changing a link

| Action | Steps | Event |
|---|---|---|
| **Disable** | The dialog warns: "Their app's list becomes the 'disabled' entry on its next refresh. Copied URIs keep working until you rotate the servers: use **Cut off** for that." Confirm → state `disabled`. | `link.disabled` |
| **Enable** | State `active`. The real output returns on the next fetch. | `link.enabled` |
| **Regenerate token** | A new token at once. The old URL returns `404` from now on. The link page shows a band "New URL ready: copy it and send it." with **Copy URL** holding the new one. | `link.token_regenerated` |
| **Change subscription** | The next fetch serves the new subscription. | `link.subscription_changed{from, to}` |
| **Set / extend / clear expiry** | Changes the expiry. Extending an expired link makes it serve the real output again. Any change re-arms the warning and the expired notification for the new expiry. | `link.expiry_changed{from, to}` |
| **Cut off** | Disable, plus rotate every server of its subscription ([credential rotation](../servers/credential-rotation.md#steps--cut-off-rotating-for-a-link)). | `link.disabled`, `link.cut_off{servers}` |
| **Delete** | The dialog explains the tombstone. Confirm → state `deleted`. The link leaves the lists, and its URL serves "⛔ Link removed" for 30 days, then `404`. | `link.deleted` |
| **Rename, edit note, change language, format override** | Plain edits. Nothing changed records nothing. | `link.changed{fields}` |

## Steps — expiry (the expiry scan, every 15 min)

1. An active link whose expiry is within 3 days (setting) and that hasn't been warned → notification "Link 'Mom — iPhone' expires on 1 Dec". → `link.expiring_soon{expiry}`
2. An active link whose expiry has passed and that isn't marked expired → marked expired. → `link.expired` (notifies)
3. Expiry itself is applied **at fetch time** by comparing with the clock, so a fetch one second after expiry already gets the stub entry. The scan only produces events and notifications.
4. Deleted links whose tombstone period has passed have their token erased, and their URL is `404` from then on.

## Rules

- Names are unique among non-deleted links.
- A token is shown only on the link page. It is never in a notification, an event or a log.
- Exactly one subscription per link.
- "Expired" isn't a separate state: it is an active link past its expiry. Clearing or extending the expiry is enough to bring it back.
- The expiry is stored as the **first moment the link is expired**: a date alone (1 Dec in Europe/Moscow) is 00:00 of the next day there; a date and time is that minute.
- A deleted link's page is read-only: "Deleted on 8 Oct 2026. Its URL serves “⛔ Link removed” until 7 Nov 2026." (or, after that, the day its tombstone ended). The tombstone period is the setting `subscriptions.tombstone` (30 days by default), the one in force at fetch time.
- A disabled link keeps its expiry. Enabling an expired link still serves the "expired" stub entry.
- Deleting is softer than it sounds and stronger than a `404`. The tombstone's stub entry makes refreshing apps drop the servers, which a `404` wouldn't (apps keep their last list on errors).
- What the link holder sees is the stub entry's name in their app, in the link's language, with the admin contact from Settings.

## Edge cases (each is a test)

- Create a link → active, unique token, URL and QR shown. The fetch serves the subscription.
- Two links with the same name → refused.
- Disable → the next fetch serves one stub entry "⛔ Link disabled · contact @tikhonp". Enable → the real output again.
- Regenerate token → the old URL `404`s at once, and the new URL serves the output.
- Expiry 2026-12-01 (date only) in Europe/Moscow → real output at 23:59:59 Moscow time, stub entry at 00:00:00 on 2 Dec.
- Expiry within 3 days → one `link.expiring_soon` notification, not repeated by the next scans.
- Expired link → the fetch serves the "⏳ Expired on 1 Dec 2026" stub entry. `link.expired` once.
- Extend an expired link's expiry → the next fetch serves the real output.
- A disabled link that also expired → serves "disabled" (disabled wins).
- Delete → gone from the Links list. Its URL serves "⛔ Link removed" for 30 days, then `404`.
- Cut off → disabled at once, and the rotations are queued for every server in its subscription.
- Change the subscription from "Family" to "Friends" → the next fetch serves Friends' servers.
- The link's language is Russian → the stub entry names are in Russian.
