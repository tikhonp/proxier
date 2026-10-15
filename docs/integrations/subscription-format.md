# Subscription format

What a link's URL returns ([fetch](../processes/subscriptions/subscription-fetch.md)). The format is de facto, defined by what client apps accept, not by a standard. Which apps take a stub entry as their new list is in [open questions](../open-questions.md#client-apps-and-the-stub-entry).

## Formats

| Format | Body |
|---|---|
| `uri-plain` | Connection URIs, one per line, `\n`-separated, with a trailing newline. |
| `uri-base64` | Standard base64 (with padding) of the `uri-plain` body. This is the v2rayN convention, which some apps prefer. |

Formats are pluggable renderers; mihomo/Clash YAML and sing-box JSON are in the [backlog](../overview.md#backlog). Each subscription chooses which formats are allowed and which is the default. A link may override the default, and `?format=` picks among the allowed ones. A browser gets the [browser page](#browser-page) instead, whatever the formats.

## Headers

| Header | Example | Meaning for apps |
|---|---|---|
| `profile-title` | `base64:0KHQtdC80YzRjw==` ("Семья") | The subscription's name in the app. Base64 keeps non-ASCII titles intact. |
| `profile-update-interval` | `12` | Hours between automatic refreshes. |
| `subscription-userinfo` | `upload=0; download=0; total=0; expire=1796158799` (end of 1 Dec 2026, Moscow time) | Apps show the expiry date. Traffic fields are 0 because traffic isn't counted. `expire=0` means no expiry. |
| `content-disposition` | `inline; filename*=UTF-8''%D0%A1%D0%B5%D0%BC%D1%8C%D1%8F.txt` | Some apps name the profile after the file. |
| `Cache-Control` | `no-store` | No caching anywhere between the app and Proxier. |

The apps tried on the real deployment honour the first four.

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

`{contact}` is the contact from Settings → General (**Admin contact**). Without one, the "· contact …" part is left out. `{date}` is the last day the link worked, in the display time zone: "1 Dec 2026" / "1 дек 2026".

The fragment (the entry's name) and the `filename*` of `content-disposition` are percent-encoded byte by byte: everything but `A–Z a–z 0–9 - . _ ~` (a space is `%20`, `@` is `%40`).

## Browser page

A browser that opens a link's URL gets a page that gets the person connected ([when](../processes/subscriptions/subscription-fetch.md#steps)). It is in the link's language and is written for someone who has never heard of VLESS:

1. The subscription's title, and "Works through {date}" when the link has an expiry.
2. Tabs for the platforms: iPhone & iPad, Android, macOS, Windows, Linux, TV. The tab for the browser's user agent is open (a TV first, then iPhone, iPad or iPod; Android or a Chromebook; Windows; Mac; Linux; anything else iPhone & iPad). iPadOS asks for pages as a Mac, so the page's script opens iPhone & iPad when a "Mac" has a touch screen. Without the script every platform's section is shown under its heading, and the tabs jump to them.
3. Each tab's apps, in order, the first recommended and drawn more prominently. Each app has a one-line note, its download buttons, and three steps: install; tap **Add to {app}**, the app's deep link that imports the link (the recommended app's is the page's primary button), or, for an app without one or when the button does nothing, copy the link and use the app's own import from the clipboard; turn it on. On the TV tab there is no Add button: the person types the link into the app.
4. **Copy link** (the clipboard, or selecting the link where the browser has no clipboard API), the URL in clear, and a QR code of it for opening the page on another device: open beside the apps on a computer, folded under them on a phone.
5. A line saying the app keeps the list up to date by itself, and **Admin contact** when set.

A link that no longer works shows only the stub's text and the contact. A subscription with no servers yet shows the full page with a note that servers will come by themselves.

The apps and their deep links (`<url>` is the link's URL, `<title>` the subscription's title):

| App | Deep link | Downloads |
|---|---|---|
| Happ | `happ://add/<url>` | iOS and macOS: App Store (not in the Russian store) and TestFlight (works with a Russian Apple ID); Apple TV: App Store (in the Russian store); Android: Google Play, APK; Windows `.exe`, macOS `.dmg`, Linux `.deb` from GitHub |
| v2RayTun | `v2raytun://import/<url>` | Android: Google Play |
| Hiddify | `hiddify://import/<url>#<title>` | Android: Google Play; every desktop and the Android APK: GitHub releases |
| v2rayNG | `v2rayng://install-sub?url=<url>#<title>` | Android and Android TV: GitHub releases |
| v2rayN | none: copy, import share links from the clipboard, update subscriptions | Windows, macOS, Linux: GitHub releases |
| Karing | `karing://install-config?url=<url>&name=<title>` | iOS and macOS: App Store (in the Russian store, free) |
| INCY | `incy://import/<url>` | iOS: App Store (in the Russian store) |
| Shadowrocket | `shadowrocket://add/sub://<base64 of url>?remark=<title>` | iOS: App Store (paid, in the Russian store) |
| Streisand | `streisand://import/<url>#<title>` | iOS: App Store (not in the Russian store) |
| Throne | `throne://addsub/<base64 of "url#title">` | Windows, macOS, Linux: GitHub releases |

The URL goes into a path as it is, since it has no `?` or `#`. In a query or a fragment, the URL and the title are percent-encoded byte by byte like the stub entry's name (a space is `%20`, never `+`). Base64 is the standard alphabet with padding, over the raw text.

| Tab | Apps, the first recommended |
|---|---|
| iPhone & iPad | Shadowrocket (known to work with Proxier's servers), Karing, INCY, Happ, Streisand |
| Android | Happ, v2RayTun, Hiddify, v2rayNG |
| macOS | Happ, Hiddify, Karing, v2rayN |
| Windows | Happ, Hiddify, v2rayN, Throne |
| Linux | Happ, Hiddify, v2rayN, Throne |
| TV | Happ (Apple TV, Android TV), v2rayNG (Android TV) |

Left out on purpose: the official sing-box apps and NekoBox, which have no XHTTP; Clash Verge Rev, Stash and Clash Mi, which need Clash YAML (a backlog format); FoXray, which is gone. The table is data in code (`subscriptions/linkpage`), like app detection. Which of these links and steps work on real devices is in [open questions](../open-questions.md#the-browser-pages-apps).

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
