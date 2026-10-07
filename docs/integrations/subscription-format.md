# Subscription format

What a link's URL returns ([fetch](../processes/subscriptions/subscription-fetch.md)). The format is de facto, defined by what client apps accept, not by a standard. The behaviour of each app must be verified during the build ([open questions](../open-questions.md)).

## Formats (first version)

| Format | Body |
|---|---|
| `uri-plain` | Connection URIs, one per line, `\n`-separated, with a trailing newline. |
| `uri-base64` | Standard base64 (with padding) of the `uri-plain` body. This is the v2rayN convention, which some apps prefer. |

Formats are pluggable renderers. Later candidates: mihomo/Clash YAML, sing-box JSON, and a browser page. Each subscription chooses which formats are allowed and which is the default. A link may override the default, and `?format=` picks among the allowed ones.

## Headers

| Header | Example | Meaning for apps |
|---|---|---|
| `profile-title` | `base64:0KHQtdC80YzRjw==` ("Семья") | The subscription's name in the app. Base64 keeps non-ASCII titles intact. |
| `profile-update-interval` | `12` | Hours between automatic refreshes. |
| `subscription-userinfo` | `upload=0; download=0; total=0; expire=1796158799` (end of 1 Dec 2026, Moscow time) | Apps show the expiry date. Traffic fields are 0 because traffic isn't counted. `expire=0` means no expiry. |
| `content-disposition` | `inline; filename*=UTF-8''%D0%A1%D0%B5%D0%BC%D1%8C%D1%8F.txt` | Some apps name the profile after the file. |
| `Cache-Control` | `no-store` | No caching anywhere between the app and Proxier. |

## Stub entry

A single connection URI that can't connect, whose name carries the message:

```
vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?encryption=none&type=tcp&security=none#%E2%9B%94%20Link%20disabled%20%C2%B7%20contact%20%40tikhonp
```

- It is served with `200` and the normal headers. Apps take it as a successful refresh and **replace** their list with it. An error or empty body would make most apps keep the old list.
- Texts (in the link's language):

| Case | EN | RU |
|---|---|---|
| disabled | ⛔ Link disabled · contact {contact} | ⛔ Ссылка отключена · пишите {contact} |
| expired | ⏳ Expired on {date} · contact {contact} | ⏳ Срок истёк {date} · пишите {contact} |
| deleted | ⛔ Link removed | ⛔ Ссылка удалена |
| no servers | ⚠️ No servers yet | ⚠️ Серверов пока нет |

`{contact}` is the admin contact from Settings. Without one, the "· contact …" part is left out.

## App detection

Each fetch's user agent is mapped to an app family for the fetch log and [shared-link alerts](../processes/subscriptions/shared-link-alerts.md). The patterns are case-insensitive and the first match wins:

| Family | User agent contains |
|---|---|
| Happ | `happ` |
| v2RayTun | `v2raytun` |
| Hiddify | `hiddify` |
| Shadowrocket | `shadowrocket` |
| Streisand | `streisand` |
| v2rayNG | `v2rayng` |
| v2rayN | `v2rayn` |
| FoXray | `foxray` |
| NekoBox / NekoRay | `nekobox`, `nekoray` |
| Karing | `karing` |
| Stash | `stash` |
| sing-box | `sing-box`, `sfi`, `sfa`, `sfm` |
| Clash / mihomo | `clash`, `mihomo`, `meta` |
| Browser | `mozilla` (and none of the above) |
| curl / scripts | `curl`, `wget`, `python`, `go-http-client` |
| Other | anything else, identified by its trimmed user agent |

The table is data in code, extended as new apps appear.
