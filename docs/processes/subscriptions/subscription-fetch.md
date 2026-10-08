# Subscription fetch

**Actors**: a client app (any app that understands subscription URLs); the public endpoint `GET /s/{token}`; servers (through `EndpointCatalog`).

This is the only thing link holders ever touch. An app requests the link's URL and receives connection URIs in the link's format, with headers that tell it the title, the refresh interval and the expiry. Outputs are computed on every fetch from current data. Nothing is cached, so a rotation or a health change is visible on the very next fetch. Format details: [subscription format](../../integrations/subscription-format.md).

## Steps

1. Rate limit: at most 60 requests per minute per client IP, counted together over `/s/`, `/r/` and `/f/` (unknown paths there count too; `/agent/` is limited per session instead). Over the limit → `429` with `Retry-After`: the whole seconds until the window ends, at least 1.
2. Look up the token by its HMAC. A token that can't be one (not 20–64 characters of `A–Z a–z 0–9 - _`) is unknown without a lookup. No match (never existed, regenerated, erased), or a deleted link past its tombstone → the platform's plain public `404` ("Not Found"), the same response as any unknown public path. Nothing is recorded.
3. Pick the format: `?format=` if it names a format the subscription allows, else the link's format override, else the subscription's default. A `?format=` naming a disallowed or unknown format → `400`.
4. Decide the output:

   | Link | Output | Fetch outcome |
   |---|---|---|
   | deleted (within tombstone) | stub "⛔ Link removed" | `stub-deleted` |
   | disabled | stub "⛔ Link disabled · contact {admin contact}" | `stub-disabled` |
   | expired | stub "⏳ Expired on {date} · contact {admin contact}" | `stub-expired` |
   | the subscription has no servers | stub "⚠️ No servers yet" | `stub-empty` |
   | otherwise | the servers' connection URIs | `ok` |

5. For `ok`:
   1. Take the subscription's servers in order, then each server's endpoints in endpoint order.
   2. If **hide unhealthy** is on, drop the servers whose health state has been a hidden state for at least the grace period.
   3. If that drops every server, keep them all and raise `subscription.all_unhealthy` (at most once an hour per subscription).
   4. Build each connection URI through the endpoint type, named with the endpoint's display name ("🇳🇱 Netherlands 1": the location as the admin entered it, whatever the link's language; the link's language changes only its stub entries).
6. Render the format and send `200` with the headers below.
7. Record the fetch: link, time, client IP, network (IPv4 /24, IPv6 /48), user agent (trimmed to 256 characters), detected app, format, outcome. Update the link's last fetch. One transaction, before the answer is written. If it fails, the error is logged (`subscriptions: record fetch`, never the token) and the answer still goes out: apps must not lose their list over a log.

## Headers

| Header | Value |
|---|---|
| `Content-Type` | `text/plain; charset=utf-8` |
| `Cache-Control` | `no-store` |
| `X-Robots-Tag` | `noindex` |
| `profile-title` | `base64:` + base64 of the subscription's title |
| `profile-update-interval` | the subscription's update interval, in hours |
| `subscription-userinfo` | `upload=0; download=0; total=0; expire=<unix time of the last second the link works, or 0>` |
| `content-disposition` | `inline; filename*=UTF-8''<percent-encoded title>.txt` |

Stub entries are served with the same headers and a `200`, so apps accept them and replace their list. The four `profile-*`, `subscription-userinfo` and `content-disposition` names are sent in lower case, as written.

## Rules

- A response never reveals whether a token exists beyond what its output says: unknown tokens all get the same `404`.
- The output is computed fresh on every request. No caching anywhere, including the gateway (`no-store`).
- The output never contains anything but connection URIs: no comments, no admin data.
- The client IP is `X-Real-IP` when the request comes from a trusted proxy, otherwise the socket address.
- `HEAD` returns the same headers as `GET` without a body, and doesn't record a fetch.
- Tokens are case-sensitive, and a trailing slash is not part of the token (`/s/{token}/` is the same URL).
- The endpoint never sets cookies and ignores any it receives.

## Edge cases (each is a test)

- Valid active link, "Family" = nl-1, de-1 → two `vless://` lines in order, named "🇳🇱 Netherlands 1" and "🇩🇪 Germany 1", plus the headers.
- The same with `?format=uri-base64` (allowed) → the base64 of the two lines.
- `?format=mihomo` (not a format) → `400`.
- A token that doesn't exist → `404`, nothing recorded.
- A regenerated link's old token → `404`.
- Disabled link → `200`, one stub entry named "⛔ Link disabled · contact @tikhonp".
- Expired link with language RU → `200`, one stub entry in Russian with the date.
- A deleted link 10 days after deletion → "⛔ Link removed". 31 days after → `404`.
- A subscription with no servers → "⚠️ No servers yet".
- Hide unhealthy on, de-1 `blocked` past the grace period → only nl-1. Both past the grace period → both, and one `subscription.all_unhealthy` per hour.
- nl-1 rotated a second ago → the new credential in the output.
- 61st request in a minute from one IP → `429`.
- A request from the tunnel with `X-Real-IP: 203.0.113.9` → the fetch records 203.0.113.9. A direct request carrying that header → the socket address is recorded.
- `HEAD` → headers only, no fetch recorded.
- The title "Семья" → `profile-title: base64:0KHQtdC80YzRjw==`.
- A user agent of 2 KB → stored trimmed to 256 characters.
