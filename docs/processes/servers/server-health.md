# Server health

**Actors**: Scheduler; check jobs (queue `checks`); the embedded proxy client; check-host.net; Admin; subscriptions (through `EndpointCatalog`); the notifier.

Health answers one question per active server: **can people in Russia use it right now, and if not, why?** Proxier runs at home in Russia, so its own proxy test is the ground truth for "usable from Russia" ([ADR 0005](../../adr/0005-home-is-the-russian-vantage-point.md)). The self-check over SSH says whether the server itself works. check-host.net nodes abroad say whether it is reachable from outside Russia. Together they separate **down** (the server is broken or gone) from **blocked** (the server is fine, but Russia can't use it).

## Checks

| Check | From | Every | Result |
|---|---|---|---|
| **Reference check** | home | 1 min | `online` (a domestic site and a foreign site both answer), `foreign-unreachable` (domestic answers, foreign doesn't), `offline` (domestic doesn't answer). Domestic: `ya.ru`, `vk.com`; foreign: `www.cloudflare.com/cdn-cgi/trace`, `www.google.com/generate_204`. Any one per side is enough. |
| **Self-check** | home, over SSH | 5 min | `ok`, `stack-failed` (SSH works, but a template check failed: which one), or `unreachable` (no SSH: timeout, refused, reset). Also collects stats. |
| **Proxy test** | home, through each endpoint | 5 min, offset from the self-check | `ok` with connect time, time to first byte and throughput, or `fail` with a class: `tcp-timeout`, `tcp-refused`, `tls-failed`, `stalled` (data stopped after N KB), `http-error`, `timeout`. |
| **External check** | check-host.net nodes: 3 in Russia, 3 abroad (Settings) | 30 min, and on demand when the proxy test fails (at most every 10 min) | TCP connect to `IP:443` per node: `ok` (ms) or `fail` (timeout, refused). |

The proxy test downloads a **256 KB** test object (default: Cloudflare's speed endpoint) through the endpoint, with a 15 s overall timeout and 5 s without data counting as a stall. It must be larger than ~20 KB, because some Russian throttling of foreign hosting networks lets the handshake and the first 16–20 KB through and then stalls. A TCP or TLS check alone would call such a server healthy.

The test URL is **configurable**. The global default is in Settings → Servers. A template can override it, for example with an object on the server's own decoy site: that tests the tunnel without depending on the server's own internet access. The override is a manifest field, `proxy_test.url`, which validation checks. Proxier warns when an object is under 64 KB, because a small object can't catch the throttling described above. The URL used is recorded with every result.

## Verdict

After each new result, Proxier evaluates the server using, for each check, the latest result that is no older than two of its intervals (self-check and proxy test 10 min, external 60 min) and not marked *inconclusive*. An older result counts as missing. The first matching rule gives the **candidate** state. "Pass" means within the thresholds unless it says "any speed". The self-check is `ok`, `stack-failed`, `unreachable`, or **missing** (no fresh result). External data is **none** (no fresh result, check-host.net unavailable, over its budget, or no node abroad configured) or the number of nodes abroad that connected.

| # | Condition | Candidate | Reason shown |
|---|---|---|---|
| 1 | Checks are paused | `paused` (at once) | "Checks paused until 18:00" |
| 2 | The reference check isn't `online` | *(no change: frozen)* | "Home internet is offline / foreign sites are unreachable from home; verdict on hold" |
| 2a | The latest self-check is `host-key-changed` | `unknown` (at once) | "Host key changed: accept it or investigate" |
| 3 | Some endpoint has no fresh proxy test | `unknown` | "Waiting for the first checks" |
| 4 | Every endpoint passes within the thresholds, and the self-check is `ok` or missing | `healthy` | none |
| 5 | At least one endpoint passes (any speed), and one is slow (first byte > 2 s or throughput < 1 Mbit/s), **or** some endpoints fail, **or** the self-check is `stack-failed` | `degraded` | "Slow: 2.4 s to first byte" / "Endpoint backup fails" / "certbot container not running" |
| 5a | Every endpoint passes and the self-check is `unreachable` | `degraded` | "The proxy works, but SSH is unreachable from home" |
| 6 | Every endpoint fails, and the self-check is `stack-failed` | `down` | "Stack broken: xray container not running" |
| 7 | Every endpoint fails, and at least one node abroad connects | `blocked` | "Reachable from Germany and Finland, but the proxy fails from home (stalled: no data for 5s after 16 KB received); 0/3 Russian nodes connect" |
| 8 | Every endpoint fails, the self-check is `ok`, and there is no external data | `blocked` | "The server is healthy, but the proxy fails from home (unconfirmed from abroad)" |
| 8a | Every endpoint fails, the self-check is `ok`, and no node abroad connects | `down` | "The stack runs, but the server is unreachable from home and from 3/3 nodes abroad" |
| 9 | Every endpoint fails, SSH is unreachable, and no node abroad connects | `down` | "Unreachable from home and from 3/3 nodes abroad" |
| 10 | Every endpoint fails, SSH is unreachable, and there is no external data | `down` | "Unreachable from home (unconfirmed from abroad)" |
| 11 | Anything else (every endpoint fails, the self-check is missing, no node abroad connects) | `unknown` | "Not enough data: …" |

Every combination of inputs matches exactly one rule. Rule 5 requires a passing endpoint; without it, rule 6's case (all fail, stack failed) would have been caught by rule 5 first. Rules 2a, 5a, 8a and 11 close the gaps the first version of this table left.

When a proxy test fails and no external result newer than 10 minutes exists, an external check is started at once and the server is marked as awaiting it. A verdict that leans on external data (rules 7, 8, 8a, 9, 10, 11) **waits** while the mark is younger than 2 minutes and no external result newer than the mark exists: it stores nothing and a delayed `servers.evaluate` looks again after 2 minutes. The external check's own evaluation, which clears the mark and cancels the delayed one, decides otherwise.

**Flap protection**: the server's health state changes only when the same candidate comes out of **2 consecutive counted evaluations** (setting). An evaluation is *counted* when it is the first to see a new proxy-test round (a result newer than the one the last counted evaluation used): the self-check's evaluation, a scheduled external check's, or a second look at the same round only refresh the detail. `paused`, a changed host key and the first state after `unknown` apply at once. Each change records `server.health_changed{from, to, reason, summary}` and sets "health since". The detail of the latest evaluation (reason, per-check summary) is kept on the server for the UI, even when nothing changed.

## Steps — check round

1. The scheduler queues the self-check (now) and the proxy test (150 s later) for every active, non-paused, non-retiring server. The self-check is skipped if the server's resource key is held by a job (a redeploy in progress shouldn't count against health); then the proxy test is not queued either. The proxy test holds no resource key (a delayed job holding it would block the round): when it starts it asks whether a job other than the round's own self-check is running on the server, and if so stores nothing and evaluates nothing.
2. Results are stored as check results with their vantage point.
3. The verdict is evaluated. A candidate is recorded, and the state changes as above.
4. A state change to `blocked` or `down`, or from `blocked`/`down` back to `healthy`, sends a notification (defaults in [events](../../events.md#servers)); `degraded`, `unknown` and `paused` stay quiet. While a server stays `blocked` or `down`, an hourly job sends a reminder when 24 h have passed since the state began or the last reminder. → `server.still_unhealthy`
5. Subscriptions with **hide unhealthy** read the state and "since" on every fetch ([fetch](../subscriptions/subscription-fetch.md)).

## Steps — home connectivity

1. The reference check runs every minute while any server is being checked.
2. The domestic site fails → `offline`. Every server's verdict is frozen, and new results are stored marked *inconclusive*. → `health.home_offline` (no notification, since Telegram is likely unreachable; shown on the dashboard). The job does nothing while no server is being checked.
3. Domestic works, but every foreign reference fails → `foreign-unreachable`, verdicts frozen. → `health.foreign_unreachable` (notifies: this is what a whitelist-only regime looks like)
4. Back to `online` → `health.home_recovered{duration}` (notifies). Every server's candidate run is cleared, so evaluations from before the outage cannot confirm anything; verdicts resume with the next results. Inconclusive results never count toward the 2 consecutive evaluations.

## Steps — pause and run now

1. **Pause checks** (1 h, 6 h, 24 h, until resumed) → state `paused` at once, no checks, no notifications. → `server.checks_paused{until}`
2. At the end, or on **Resume**, the state becomes `unknown` until new results arrive, and a check round is queued at once. → `server.checks_resumed`. The end of a timed pause is a delayed `servers.resume` job without a resource key. It resumes only when the stored end has passed, so the job of an earlier pause, or one after a manual **Resume** (which cancels it), does nothing to a later pause.
3. **Run checks now** queues a full round (self-check, proxy test, external check) right away; the external check is subject to its budget and the page says so when it is refused. The Health tab polls every 2 s for a minute to show the results as they arrive.

## The Health tab

- The verdict, with its reason as one sentence and "since".
- A matrix of the latest results. Rows: home internet, self-check (with each template check), proxy test per endpoint (connect, first byte, throughput), each Russian node, each node abroad. Columns: result, time, 24 h success strip (48 cells of 30 minutes).
- A timeline of health states (24 h / 7 d / 30 d).
- **Run checks now**, **Pause checks**.

## Rules

- Only active servers have health. Provisioning, failed and retired servers show their lifecycle state instead.
- Health never changes a subscription's configuration. A subscription with **hide unhealthy** only leaves the server out of its output, and only after its grace period.
- A changed host key makes the self-check fail with `host-key-changed`, not `unreachable`. The verdict becomes `unknown` with the reason "host key changed — accept or investigate", and the security notification is sent ([ADR 0007](../../adr/0007-agentless-ssh-with-pinned-host-keys.md)).
- External checks respect check-host.net's limits: per server a scheduled check is refused when the last real one is newer than 80 % of the interval (24 min of 30), an on-demand one when it is newer than 10 min, and a global cap per rolling hour (setting) applies. A refused check stores one `skipped` row. When check-host is unavailable (or skipped), there is no external data, rules 8 and 10 apply and the reasons say "unconfirmed".
- Every threshold and interval named here is a setting.

## Edge cases (each is a test)

- New active server, first proxy test passes, self-check ok → `healthy` at once (the first state after `unknown`).
- Healthy server, one failed proxy test, then a passing one → still `healthy` (flap protection).
- Two consecutive evaluations where the proxy stalls after 16 KB, self-check ok, and 2/3 nodes abroad connect → `blocked`, with the stall in the reason, and one notification.
- Proxy fails, self-check ok, check-host disabled → `blocked (unconfirmed)` after two evaluations.
- Proxy fails, self-check ok, no node abroad connects → `down` ("the stack runs, but the server is unreachable").
- Proxy fails and the self-check has no fresh result, no external data → `unknown` ("not enough data").
- Proxy passes but SSH is unreachable → `degraded` ("the proxy works, but SSH is unreachable").
- One failed proxy test followed by the round's self-check (which sees the same result) and then a passing test → still `healthy`; only the first evaluation of a new proxy round counts.
- Proxy fails, SSH times out, 0/3 nodes abroad connect → `down` ("unreachable").
- Proxy fails, SSH works, the xray container is exited → `down` ("stack broken: xray container not running").
- Proxy passes but takes 2.4 s to first byte → `degraded`, with no notification by default.
- Proxy passes, but the certbot container is down → `degraded` ("certbot container not running").
- Certificate has 10 days left, everything else fine → `healthy`, plus one `server.cert_expiring` notification per crossing of the 14-day threshold (not daily; it is raised again only after the certificate was renewed and runs low again). The same goes for `server.disk_low` below 10 % free.
- Home's domestic reference fails → every verdict frozen, `health.home_offline`, and no server changes state or notifies.
- Foreign references fail while domestic ones work → verdicts frozen and one `health.foreign_unreachable` notification. Back to `online` → `health.home_recovered`.
- After home recovers, the first evaluation of a server whose proxy now fails → only a candidate. The state changes on the second.
- A server `blocked` for 25 hours → one `server.still_unhealthy` reminder at 24 h.
- A redeploy running when the check round is due → the round is skipped for that server, and the state is unchanged.
- **Pause checks** for 1 h → `paused` at once, no checks for an hour, then `unknown`, then a verdict from new results.
- The proxy test fails, and the last external result is 25 min old → an on-demand external check starts, and the evaluation waits for it (at most 2 min).
- check-host answers with an error → treated as "no external data", and the reason says "unconfirmed".
- A host key changed on the server → `unknown` with "host key changed", plus the security notification. No `down`.
- A server with two endpoints where one fails and one passes → `degraded` ("Endpoint backup fails").
