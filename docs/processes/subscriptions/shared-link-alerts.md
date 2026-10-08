# Shared-link alerts

**Actors**: the shared-link scan (queue `maintenance`, every 15 min); Admin; the notifier.

A link is meant for one person or device. If it is fetched from many networks or by many different apps, it has probably been passed on. Credentials are shared, so Proxier can't see who actually connects. What it does see is who fetches each link, and that is enough to notice sharing and act: regenerate the token, disable, or cut off.

## Steps

1. Every 15 minutes, for each link with fetches in the last 24 hours (whatever its state), count over those 24 hours:
   - **distinct networks**: IPv4 addresses grouped by /24, IPv6 by /48, so a phone hopping between addresses of one carrier counts once;
   - **distinct apps**: by detected app family ([subscription format](../../integrations/subscription-format.md#app-detection)). Unknown user agents count by their trimmed text.

   The scan is the job `subscriptions.shared_scan` (queue `maintenance`, quiet). The window is the 24 hours up to the scan; a link without fetches in it isn't looked at.
2. A link over its thresholds (defaults: **more than 4 networks** or **more than 3 apps**; per-link overrides allowed) that hasn't alerted in the last 24 hours, and isn't muted, raises an alert. → `link.shared_suspected{networks, apps, window: 24h}` (notifies: "Alex looks shared · Fetched from 6 networks and 3 apps in 24 h.")
3. The notification opens the link page. While the link **has an alert** (alerted within the last 24 hours and not muted) the page shows a gold band with the counts of the last 24 hours **as they are now** (not those of the alert), the limits, and the actions **Regenerate token**, **Disable**, **Cut off**, **Raise limits**, **Mute alerts**, each with its consequence. The Links list marks such a link "looks shared" and filters it with **With alerts**; the dashboard lists it. **Who fetches it** shows the distinct networks (with country) and apps over 24 h or 7 d, muted or not, above the fetch log.

## Rules

- Alerts are about fetches only. A link shared by copying its connection URIs out of the app can't be detected; that is a limit of shared credentials ([ADR 0004](../../adr/0004-one-shared-credential-per-endpoint.md)).
- At most one alert per link per 24 hours. The counts in the next alert are fresh.
- Thresholds are global settings. A link can override them (a family iPad used by three people) or mute alerts entirely, on its **Alert limits** page (`link.changed{fields: alert limits / alerts muted}`). Raising the limits doesn't end an alert already raised; it ends after its 24 hours.
- **Countries** come from looking up each network once a month by its **first address** (`198.51.100.0`, `2001:db8:4f2::`, never the person's own address) through `subscriptions.network_country_url` (default ipinfo.io; empty turns countries off). A fetch from a network without a known country queues the lookup job `subscriptions.network_countries` (later fetches merge into it; at most 50 networks a run). A failed lookup is stored as unknown and tried again after a day.
- Stub fetches (disabled, expired, deleted) are counted too: someone still fetching a disabled link from five networks is worth knowing about.
- The notification shows counts only, never IPs. IPs are on the link page.
- Fetch records are kept `subscriptions.fetch_retention` (90 days).

## Edge cases (each is a test)

- One phone on mobile data, fetching from 10 addresses of the same /24 → 1 network, no alert.
- Fetches from 5 different /24 networks in 24 h → alert (more than 4).
- 4 networks and 4 apps (Happ, v2RayTun, Hiddify, Shadowrocket) → alert (more than 3 apps).
- An alert raised at 10:00, still over at 10:15 → no second alert until 10:00 next day.
- Per-link override "networks 10" with 6 networks → no alert.
- A muted link with 20 networks → no alert. The counts still show on its page.
- Fetches spread across 25 hours (3 networks in the first hour, 2 in the last) → at most 3 within any 24 h window, so no alert.
- A disabled link fetched from 6 networks → alert.
- Two unknown user agents "okhttp/4.12" and "Dalvik/2.1" → counted as 2 apps.
- A link with no fetches in 24 h → not evaluated.
