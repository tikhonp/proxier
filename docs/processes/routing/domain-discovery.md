# Domain discovery

**Actors**: Admin; the discovery job (queue `discovery`); the catalog's reverse index; the Chromium sidecar; servers (through `ProxyDialer`, for visits through a server).

The admin types a website, and Proxier finds the domains it uses so they can be tunneled. First it checks whether a known list already covers the site (catalog lookup). Then it opens the site in a headless browser and records every hostname the page loads (headless visit). The admin ticks what to keep, and it becomes a custom service. Nothing changes until the admin saves.

## Steps — starting a run

1. Routing → **Discover**. Fields:
   - **Website**: a URL or a domain.
   - **Visit through**: **Direct** (from home, the default); a chosen healthy server; or **Auto**, which goes direct and repeats through the first healthy server **by name** if the page doesn't load or the direct visit had failed requests. Unhealthy servers are listed but can't be chosen. Without the servers module only Direct is offered.
   - **Depth**: **Home page** (default), or **Home page + up to 5 links** on the same site.
2. **Discover** queues the job and opens the run page, which fills in as steps finish.

## Steps — the job

1. **Normalise**: take the host and find its registrable domain with the public suffix list (`www.claude.ai` → `claude.ai`; `me.github.io` stays `me.github.io`, because `github.io` is a public suffix).
2. **Catalog lookup**: search the reverse index for the host, the registrable domain and their parent domains. Show every selector containing them: exact, suffix, or a suffix above. Each suggestion shows its domain count and whether it is already a service and in which lists. This step takes milliseconds and is shown first.
3. **Headless visit** (when Chromium is configured):
   1. Open a fresh, isolated browser context: no cookies, no storage. For **through a server**, the context's proxy is a SOCKS5 listener Proxier opens for this visit only, on the address that routes to the Chromium sidecar, relaying every connection through `ProxyDialer` (that server's endpoint). Chrome can't authenticate to a SOCKS proxy, so the listener accepts connections **only from the sidecar's address**. Names reach the server unresolved, so they resolve at its end.
   2. Load the page. Wait until the network has been idle for 2 s (at most 30 s), scroll to the bottom, then wait 3 s more.
   3. Record every request, service-worker and websocket ones included: host, resource type, status, redirect chain, and failure (DNS error, refused, reset, timeout).
   4. Take a screenshot of the top of the page, scrolled back up (JPEG, 1280×800). Screenshots are files, `<data dir>/discovery/<run>/<visit>-<page>.jpg`, not rows.
   5. For depth > 0: pick up to 5 distinct links to the same registrable domain and visit each the same way.
   6. Limits: 90 s per page, 300 distinct hostnames per run.
4. **Classify** each hostname:

   | Class | Rule | Default |
   |---|---|---|
   | First-party | Same registrable domain as the website | ticked, as a suffix domain of the registrable domain |
   | Failed directly | Any class, when its requests failed on a direct visit (a strong sign it is blocked) | ticked, with the failure reason shown |
   | CDN / infrastructure | Under a built-in list of CDN and platform suffixes: `cloudflare.com`, `cloudflare.net`, `cloudfront.net`, `akamai.net`, `akamaihd.net`, `akamaized.net`, `edgekey.net`, `edgesuite.net`, `fastly.net`, `fastly.com`, `fastlylb.net`, `gstatic.com`, `googleapis.com`, `googleusercontent.com`, `ggpht.com`, `jsdelivr.net`, `unpkg.com`, `bootstrapcdn.com`, `azureedge.net`, `azurefd.net`, `msecnd.net`, `b-cdn.net`, `cdn77.org`, `amazonaws.com`, `yastatic.net`, `recaptcha.net`, `hcaptcha.com` | not ticked |
   | Tracker / ads | Under a built-in list of analytics, tag managers and ad networks: `google-analytics.com`, `googletagmanager.com`, `googlesyndication.com`, `googleadservices.com`, `doubleclick.net`, `facebook.net`, `hotjar.com`, `segment.io`, `segment.com`, `mixpanel.com`, `amplitude.com`, `sentry.io`, `nr-data.net`, `clarity.ms`, `bat.bing.com`, `cloudflareinsights.com`, `mc.yandex.ru`, `mc.yandex.com`, `top-fwz1.mail.ru`, `counter.yadro.ru`, `scorecardresearch.com`, `criteo.com`, `criteo.net`, `adnxs.com`, `taboola.com`, `outbrain.com` | not ticked |
   | Third-party | Anything else | not ticked |
   | IP literal | A request straight to an IP address | listed separately, can't be ticked (domains only) |

   Every hostname is also marked **covered by** when a service in the routing lists already covers it.
5. Finish → `routing.discovery_completed{website, hosts, suggestions}`. When the visit itself can't run (Chromium unreachable, a DevTools error, the server's dialer gone) → `routing.discovery_failed{website, error}`, and the catalog suggestions still show. A page that never loads is **not** a failure of the run: its visit says so ("the page never loaded: ERR_CONNECTION_RESET"), its hosts are marked failed, and Auto repeats it. The site's own class wins over the built-in lists (a run of `cloudflare.com` finds first-party hosts).

## Steps — using the result

1. The run page shows:
   - the suggestions;
   - the screenshots, so a block page ("Доступ ограничен") or a captcha is obvious;
   - a table grouped by registrable domain: a checkbox, the domain, suffix or exact (registrable domain as suffix by default, expandable to individual hostnames as exact), class, request count, failures and their reasons, covered by.
2. Actions:
   - **Use suggestion** adds the suggested selector to chosen lists through the normal add flow ([service management](./service-management.md)).
   - **Create custom service** from the ticked rows: name from the page title or the domain, tag a slug of it, choose routing lists. It saves through the custom-service flow, with its normalisation and the server-hostname guard.
   - **Add to an existing custom service** merges the ticked rows into one, through the same flow.
   - **Visit again through…** starts a new run with another path, for example through a server when the direct visit showed a block page.

## Rules

- Discovery never changes routing by itself. Only the save actions do, through the normal flows.
- The browser never signs in, never fills forms and never accepts cookie banners. It sees what an anonymous visitor sees. Logged-in app pages are out of reach in the first version (HAR import is in the backlog).
- Each run uses its own browser context, so nothing carries over between runs.
- A visit through a server goes out from that server's IP, as a client of its endpoint would. A direct visit goes out from home.
- Without Chromium configured, a run is the catalog lookup only, and the page explains how to enable visits.
- Runs and their screenshots are kept 30 days.
- One run at a time (queue concurrency 1). Others wait in the queue.

## Edge cases (each is a test)

- `claude.ai` → the catalog suggests `v2fly:anthropic` (contains `claude.ai`) before the visit finishes.
- `https://www.example.com/some/page?x=1` → normalised to host `www.example.com`, registrable domain `example.com`.
- `me.github.io` → the registrable domain is `me.github.io`, not `github.io`.
- A site blocked from home, visited directly → the page fails to load or shows a block page in the screenshot. Its hosts are marked "failed directly" and ticked. **Visit again through nl-1** loads it.
- **Auto** with a failing direct visit → the run repeats through the first healthy server and shows both attempts.
- A page that loads `www.google-analytics.com` → class tracker, not ticked.
- A page that loads `cdn.jsdelivr.net` → class CDN, not ticked.
- A request to `203.0.113.5` → listed under IP literals, can't be ticked.
- 450 distinct hostnames (a heavy news site) → stops at 300 with a note.
- Chromium not configured → only the catalog lookup runs, with the explanation.
- Chromium unreachable mid-run → `routing.discovery_failed`, and the suggestions stay.
- **Create custom service** with a ticked `tikhonnnnn.com` (covers a server hostname) → refused by the guard, naming the server.
- Two runs started together → the second waits until the first finishes.
- A run 31 days old → deleted with its screenshots.
