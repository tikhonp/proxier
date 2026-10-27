# Subscription format

What a link's URL returns ([fetch](../processes/subscriptions/subscription-fetch.md)). The format is de facto, defined by what client apps accept, not by a standard. Which apps take a stub entry as their new list is under [stub entry](#stub-entry); the apps not tried yet are in [open questions](../open-questions.md#client-apps-and-the-stub-entry).

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
vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?encryption=none&type=tcp&security=none#%E2%9B%94%20Link%20disabled%20%C2%B7%20%40tikhonp
```

- It is served with `200` and the normal headers. Apps take it as a successful refresh and **replace** their list with it. An error or empty body would make most apps keep the old list.
- Seen on real devices: Shadowrocket, Hiddify, Happ (Android 4.9.0) and v2rayNG (2.3.11) replace their list with the stub. v2rayNG shows its whole name. Happ fits about 27 characters of a name on a 360 px phone, so the stub names the contact without a word before it ("⛔ Ссылка отключена · @tikhonp"), and a long contact may still lose its end there. On a `404` (a regenerated token), Happ, v2rayNG and Hiddify (Android 4.1.1) keep their last list and show an error: Happ a toast with the status, v2rayNG a count of failed updates, Hiddify a dialog with the HTTP exception. v2rayNG refreshes only the group whose tab is open.
- Texts (in the link's language):

| Case | EN | RU |
|---|---|---|
| disabled | ⛔ Link disabled · {contact} | ⛔ Ссылка отключена · {contact} |
| expired | ⏳ Expired on {date} · {contact} | ⏳ Истекла {date} · {contact} |
| deleted | ⛔ Link removed | ⛔ Ссылка удалена |
| no servers | ⚠️ No servers yet | ⚠️ Серверов пока нет |

`{contact}` is the contact from Settings → General (**Admin contact**). Without one, the "· {contact}" part is left out. `{date}` is the last day the link worked, in the display time zone: "1 Dec 2026" / "1 дек 2026".

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
| Hiddify | `hiddify://import/<url>#<title>` | iOS: App Store (not in the Russian store); Android: Google Play; every desktop and the Android APK: GitHub releases |
| v2rayNG | `intent://install-sub?url=<url>#<title>#Intent;scheme=v2rayng;package=com.v2ray.ang;S.browser_fallback_url=<its releases>;end` | Android and Android TV: GitHub releases |
| INCY | `incy://import/<url>` | iOS: App Store (in the Russian store) |
| Shadowrocket | `shadowrocket://add/sub://<base64 of url>?remark=<title>` | iOS, macOS and Apple TV: App Store (paid, in the Russian store) |

The URL goes into a path as it is, since it has no `?` or `#`. In a query or a fragment, the URL and the title are percent-encoded byte by byte like the stub entry's name (a space is `%20`, never `+`). Base64 is the standard alphabet with padding, over the raw text.

Seen on an Android phone (Chrome, 360 px): the Android tab opens by itself, and **Add to Hiddify**, **Add to Happ** and **Add to v2rayNG** each open their app and add the link under the subscription's title (Hiddify straight after its first-run screen). Hiddify also claims `v2rayng://`, so with both installed Android asked which app opens a plain `v2rayng://install-sub` link: v2rayNG's button is Android's `intent:` form of it, which names the app (Chrome opened v2rayNG straight away, and the group took the title, Cyrillic included). The manual imports match what the apps show: Happ's + has "Вставить из буфера обмена", v2rayNG's + "Импорт из буфера обмена" (it names that group "import sub": it reads neither `profile-title` nor the file name), and Hiddify's + a "Буфер обмена" tile. **Copy link** fills the clipboard, and the QR code is folded under the apps. Chrome on Android also has Google fetch a page it opens (user agent `… Google-Read-Aloud …`, from Google's addresses): it asked for `*/*`, so it got the connection URIs and counted as another network. Such fetches are a **Link preview**: they get the page and count toward no alert.

| Tab | Apps, the first recommended |
|---|---|
| iPhone & iPad | Shadowrocket (known to work with Proxier's servers), Hiddify, INCY, Happ |
| Android | Hiddify, Happ, v2rayNG |
| macOS | Shadowrocket, Hiddify, Happ |
| Windows | Hiddify, Happ |
| Linux | Hiddify, Happ |
| TV | Shadowrocket (Apple TV), Happ (Apple TV, Android TV), v2rayNG (Android TV) |

Left out on purpose: the official sing-box apps and NekoBox, which have no XHTTP; Clash Verge Rev, Stash and Clash Mi, which need Clash YAML (a backlog format); FoXray, which is gone. The apps, their order on each tab, notes, download buttons, manual imports (in English and Russian) and deep links (Go templates over the URL and the title) are one file, `internal/modules/subscriptions/linkpage/apps.yaml`: adding, removing or reordering an app changes only it. It is compiled in and checked when the package loads (every tab once with an app, known apps, labels and manual imports, both languages, https downloads, deep links that render), so a mistake in it fails the tests. Which of these links and steps work on real devices is in [open questions](../open-questions.md#the-browser-pages-apps).

## App detection

Each fetch's user agent is mapped to an app family for the fetch log and [shared-link alerts](../processes/subscriptions/shared-link-alerts.md). The patterns are case-insensitive and the first match wins. **Link preview** is a server fetching the URL for a person who opened or pasted it (Chrome's read-aloud, a messenger's preview): it comes first because those user agents also name Mozilla. It always gets the browser page, never the URIs, and counts toward no alert:

| Family | User agent contains |
|---|---|
| Link preview | `google-read-aloud`, `telegrambot`, `whatsapp`, `facebookexternalhit`, `slackbot`, `discordbot`, `twitterbot`, `linkedinbot`, `skypeuripreview`, `vkshare` |
| Happ | `happ` |
| v2RayTun | `v2raytun` |
| Hiddify | `hiddify` |
| Shadowrocket | `shadowrocket` |
| Streisand | `streisand` |
| v2rayNG | `v2rayng` |
| v2rayN | `v2rayn` |
| FoXray | `foxray` |
| Throne | `throne` |
| NekoBox / NekoRay | `nekobox`, `nekoray` |
| Karing | `karing` |
| INCY | `incy` |
| Stash | `stash` |
| sing-box | `sing-box`, `sfi`, `sfa`, `sfm` |
| Clash / mihomo | `clash`, `mihomo`, `meta` |
| Browser | `mozilla` (and none of the above) |
| curl / scripts | `curl`, `wget`, `python`, `go-http-client` |
| Other | anything else, identified by its trimmed user agent |

The table is data in code, extended as new apps appear.
