# Events

Every state change records an event in the same transaction as the change ([architecture](./architecture.md#events-and-history)). Events are the activity history (Activity page, the Activity tab on each object) and the only input to notifications. The tables below are the full catalog for the first version. **Notify** is the default of the notification rule, which the admin can turn on or off in Settings → Notifications.

The payload lists the fields beyond the subject. Notification texts are given in English; each has a Russian twin in the catalogs.

## Platform

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `admin.created` | admin | — | off | from `proxier manage create-admin` |
| `auth.signed_in` | admin | ip, user agent, new_ip | **on when `new_ip`** | "Signed in from a new IP 203.0.113.7 (Chrome on macOS)" |
| `auth.sign_in_failed` | — | ip, username tried | off | |
| `auth.locked` | — | ip, failures, minutes | **on** | "5 failed sign-ins from 203.0.113.7; locked for 15 min" |
| `auth.password_changed` | admin | — | **on** | |
| `auth.signed_out` | admin | session (id), by (`self` / `settings`) | off | |
| `auth.signed_out_everywhere` | admin | sessions ended | off | |
| `admin.language_changed` | admin | from, to | off | |
| `settings.changed` | setting group | keys changed (never values) | off | |
| `ssh.key_generated` | ssh key (`ssh:identity`) | fingerprint, regenerated | **on when `regenerated`** | The first start generates the key silently; **Regenerate** notifies, because every server, jump host and router needs the new key. |
| `ssh.host_key_pinned` | known host | address, fingerprint | off | First contact with an address (subject `ssh_host:<address>`). |
| `ssh.host_key_changed` | known host | address, old and new fingerprint | **on** | All work with that host stops until accepted. Recorded once per new key, however many jobs meet it. |
| `ssh.host_key_accepted` | known host | address, old and new fingerprint | off | |
| `ssh.host_forgotten` | known host | address, fingerprint | off | By the admin, or when the servers module retires a server. |
| `job.failed` | job | type, error | **on** for job types without their own failure event | |
| `notification.failed` | event (`event:<id>`) | channel, error | off, and never notifiable | A delivery gave up after its last retry. Shown on the dashboard. |
| `backup.completed` | job | file, size | off | |
| `backup.failed` | job | error | **on** | The job's own failure event, so no `job.failed`. The message links to the job. |

## Servers

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `template.created` | template | — | off | |
| `template.version_published` | template | version, warnings (their number), messages | off | |
| `template.default_version_changed` | template | from, to | off | |
| `template.archived` | template | slug | off | |
| `template.unarchived` | template | slug | off | |
| `template.deleted` | template | slug, name | off | A template that never built a server. |
| `template.changed` | template | fields (which of name, slug, description changed) | off | A rename or a new description. The slug changes only before the first version. |
| `template.draft_discarded` | template | based_on, revision | off | **Discard draft**. |
| `template.agent_session_opened` | template | expires_at, context | **on** | "An agent can edit the nginx-xhttp draft until 18:41". Anyone holding the copied prompt can do so. |
| `template.draft_saved` | template | by (admin / agent), files (the changed paths, at most 50), changed (their number), from_version (when the draft was created from a version), source (zip / git, for an import) | off | A save that changes no file records nothing. Also recorded when a draft is created from a version, from the skeleton or by an import. |
| `template.draft_validated` | template | by, errors, warnings | off | |
| `template.agent_session_closed` | template | reason (expired / revoked / published / discarded / replaced), saves, session | off | `replaced`: a new hand-off for the same draft ended it. The actor of a save or validation by the agent is `agent:<session id>`. |
| `location.created` | location | code, name, country | off | |
| `location.changed` | location | fields (`name`, `country`) | off | Only when something changed. |
| `location.deleted` | location | code, name | off | Only a location without servers. |
| `server.created` | server | IP, template version | off | |
| `server.provisioning_failed` | server | step, error | **on** | "nl-2: setup failed at 'Issue certificate': …" |
| `server.activated` | server | proxy test timings, forced | **on** | "nl-2 is ready (🇳🇱 Netherlands 2)"; when forced by **Activate anyway**: "nl-2 is active without a passing proxy test" |
| `server.redeployed` | server | kind (`redeploy` / `upgrade` / `params` / `restore` / `restart` / `images` / `reboot`), from version, to version, files changed; `rollout` (id) when a rolling upgrade ran it; `changed_images` for `images` | off | Rotation records `server.credentials_rotated` instead. |
| `server.redeploy_failed` | server | kind (also `rotate`), step, error; `rollout` when a rolling upgrade ran it; `restored` (kind `rotate`: whether the old files were put back); `cancelled` | **on**, except when `cancelled` | A cancelled job fails its deployment and records this with `cancelled: true`; the rollout still sees it. |
| `server.credentials_rotated` | server | keys rotated | **on** | |
| `server.health_changed` | server | from, to, reason, check summary | **on** for changes to `blocked`, `down`, and back to `healthy`. **off** for `degraded` and `unknown`; back to `healthy` only from `blocked` or `down` | "🔴 de-1 is down: unreachable over SSH and from 3/3 nodes abroad" |
| `server.still_unhealthy` | server | state, since | **on** | Reminder every 24 h while blocked or down. |
| `server.cert_expiring` | server | days_left | **on** | Once per crossing of the 14-day threshold (not daily): raised again only after the certificate was renewed and runs low again. |
| `server.disk_low` | server | free_pct | **on** | Under 10 % free, once per crossing. |
| `server.checks_paused` | server | until | off | |
| `server.checks_resumed` | server | — | off | |
| `server.retired` | server | dns_removed (names), dns_kept (name, reason), stack_removed | off | Recorded by the retire job's last step. |
| `server.retire_failed` | server | step, error, cancelled | **on** | "nl-2: retirement failed at …". The server stays out of service (retiring) until the admin retries; a cancelled job notifies nothing. |
| `server.notes_changed` | server | — | off | Only when the notes changed. |
| `server.rollout_started` | rollout (`rollout:<id>`) | servers (names), to_version | off | A rolling upgrade began. |
| `server.rollout_finished` | rollout | state (`done` / `stopped` / `cancelled`), done, failed, not_started, skipped | off | Recorded once, when the rollout is no longer running and no server of it is. |
| `health.home_offline` | — | reference check results | off | Telegram is unreachable anyway. It's shown on the dashboard. |
| `health.foreign_unreachable` | — | reference check results | **on** | "Foreign internet is unreachable from home; server verdicts are on hold" |
| `health.home_recovered` | — | duration | **on** | |

## Subscriptions

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `subscription.created` / `.deleted` | subscription | name | off | |
| `subscription.updated` | subscription | changes (the changed settings, comma-joined: name, title, description, formats, default format, update interval, hide unhealthy, hidden states, grace, auto add) | off | Nothing is recorded when nothing changed. |
| `subscription.servers_changed` | subscription | added, removed (names, comma-joined, `""` for none), reordered (bool); `auto: true` for the automatic add; `reason: "retired"` | off | Includes removals caused by retirement, one event per subscription. |
| `subscription.all_unhealthy` | subscription | servers (names, comma-joined), count | **on** | Hiding would leave the output empty, so every server is served ([fetch](./processes/subscriptions/subscription-fetch.md)). Raised by a fetch (actor `system`), at most hourly per subscription. |
| `link.created` | link | subscription (name), expiry | off | |
| `link.changed` | link | fields (of name, note, language, format, alert limits, alerts muted) | off | The plain edits of a link. |
| `link.disabled` / `link.enabled` | link | — | off | |
| `link.token_regenerated` | link | — | off | |
| `link.subscription_changed` | link | from, to (names) | off | |
| `link.expiry_changed` | link | from, to | off | |
| `link.expiring_soon` | link | expiry | **on** | Once per expiry, `subscriptions.expiry_warning` (3 days) before. Not sent when the link expires before the scan saw it. |
| `link.expired` | link | expiry | **on** | Once per expiry, by the expiry scan; a disabled link notifies once it is enabled again. |
| `link.deleted` | link | — | off | |
| `link.cut_off` | link | servers, skipped (names) | off | The rotations record their own events. |
| `link.shared_suspected` | link | networks, apps, window | **on** | At most once per link per 24 h; never names an IP. |

## Routing

Every type is declared from [3a](./build/3a.md#events); the sub-phase in brackets is the one that first records it. Lists in payloads are one comma-joined string.

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `routing.service_added` | service | selector (`""` custom), tag, source, origin | off | 3a |
| `routing.service_updated` | service | changes (of source, name, tag, description, domains), from, to (selectors for a switch, tags for a rename), added, removed (names, for a domains change) | off | 3a |
| `routing.service_removed` | service | selector, tag | off | 3a |
| `routing.snapshot_accepted` | service | added, removed, suffix, exact, forced, in_round | off: the digest carries it | 3c |
| `routing.snapshot_rejected` | service | reason (`empty`, `shrink`), old_count, new_count, lost_pct, in_round | **on** outside the daily round 🟡 | 3c. "netflix: new snapshot held back" / "It has 83 domains instead of 212 (a 61 % drop). Targets keep the old list until you decide." The same rejection again (equal to the one waiting) records nothing. |
| `routing.snapshot_dismissed` | service | new_count, automatic (true: a refresh equal to the accepted snapshot ended it; false: **Dismiss**) | off | 3c |
| `routing.refresh_failing` | service | failures, error | **on** at 3 🔴 | 3c. Once per run of failures; a success resets. "netflix: refresh failing" / "3 failures in a row: HTTP 404. Targets keep the last good list." |
| `routing.refresh_digest` | `routing:refresh` | changed, added, removed, rejected, failing (started failing this round), still_failing, services (how many the round refreshed) | **on** when changed + rejected + failing > 0 📋 | 3c. Recorded after every daily round; a service failing for days is news on its first day only. "Routing refresh: 3 services changed (+42 / −5 domains)" or "Routing refresh: no changes" / "1 held back · 1 started failing · 2 still failing" (each part only when not zero). |
| `routing.catalog_refreshed` | `routing:catalog` | v2fly, iplist_main, iplist_beta, iplist_russia (entries; −1 for a source that failed) | off | 3c. Only when a source wrote a new generation or failed; an unchanged catalog records nothing. |
| `routing.catalog_refresh_failed` | `routing:catalog` | source, error, since | **on** after 3 failures in a row (3 days) 🔴 | 3c. Once per run of failures. "Catalog: v2fly failing since 6 Oct" / "HTTP 502. Search shows its catalog of 5 Oct." |
| `routing.list_created` | routing list | name | off | 3b |
| `routing.list_updated` | routing list | changes (`services`, `order`, `default`, or the fields: `name`, `description`); added or removed (tags, comma-joined), reordered (true), from, to (old and new name) | off | 3b. Nothing when nothing changed. |
| `routing.list_deleted` | routing list | name, moved (the targets moved to another list, comma-joined) | off | 3b. Each moved router records `router_updated{changes: list, from, to}`, each config `shadowrocket_updated{changes: list, from, to}`. |
| `routing.list_refused_server_hostname` | routing list | domain, server, hostname, service | off | 3b. Shown in the UI at once; one per list the refused change would have broken, recorded after its transaction rolled back. A refresh that brings such a name records nothing (the list shows a warning while it lasts). |
| `routing.shadowrocket_created` | Shadowrocket config | name, list, policy | off | 3d; also by the mtvpn import |
| `routing.shadowrocket_updated` | Shadowrocket config | changes: `base` (+ version), `list` (+ from, to), `policy` (+ policy_from, policy), `list, policy`, `token`, `disabled`, `enabled` | off | 3d. Never the token. An edit that changes nothing records nothing. |
| `routing.shadowrocket_deleted` | Shadowrocket config | name, list | off | 3d |
| `routing.router_added` | router | list, host, by (admin / routerscripts) | off | 3e |
| `routing.router_updated` | router | changes (of name, connection, names, list), from, to (list names) | off | 3e |
| `routing.router_connected` | router | version, board | off | 3e. First successful connection; ends "awaiting setup". |
| `routing.router_synced` | router | added, updated, removed, recorded, trigger | off | 3e |
| `routing.router_sync_failed` | router | step, error, consecutive, manual, attempt, final | **on** when the job gives up (final) 🔴 | 3e |
| `routing.router_recovered` | router | failures, notified | **on** after a notified failure 🟢 | 3e |
| `routing.router_paused` / `.resumed` | router | — | off | 3f |
| `routing.router_removed` | router | name, cleaned | off | 3f |
| `routing.drift_detected` | router | tags, repair | **on** only when repair is off 🟡 | 3f |
| `routing.unmanaged_tags_found` | router | tags (the new ones) | **on** once per new set 🟡 | 3f |
| `routing.unmanaged_tag_ignored` | router | tag, ignored | off | 3f |
| `routing.discovery_completed` / `.failed` | discovery run | website, hosts, suggestions / website, error | off | 3g. The admin is watching the run. |

## Router scripts

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `routerscript.created` | router script | — | off | |
| `routerscript.version_published` | router script | version, parameters | off | |
| `routerscript.generated` | generation | version, router name, link, router | off | |
| `routerscript.fetch_url_created` | generation | expires | off | |
| `routerscript.fetched` | generation | IP, user agent | **on** | "fresh-router v4 for 'Parents' was fetched from 198.51.100.4" |
| `routerscript.fetch_url_expired` | generation | — | off | It was never used. |

## Platform notification texts

Titles and sentences of the events the platform notifies about; each has a Russian twin in the catalog (`notify.<type>` and `notify.<type>.body`). `{subject}` is the subject's name when the message is queued; of `job.failed` it is the job's name, e.g. "Job #421 · Demo job".

| Event | Emoji | Title | Sentence |
|---|---|---|---|
| `auth.signed_in` (`new_ip`) | 🔐 | Signed in from a new IP {ip} | {browser} |
| `auth.locked` | 🔐 | {failures} failed sign-ins from {ip} | Locked for {minutes} min. |
| `auth.password_changed` | 🔐 | The admin password was changed | Via {via}. |
| `ssh.key_generated` (`regenerated`) | 🔐 | Proxier's SSH key was regenerated | Install the new key on every server, jump host and router. |
| `ssh.host_key_changed` | 🔐 | Host key of {subject} changed | Work with it is stopped until you accept the new key. |
| `job.failed` | 🔴 | {subject} failed | {error} |
| `backup.failed` | 🔴 | Backup failed | {error} |

An event without a subject (a lockout) links to Activity; any other links to its subject's page.

## Notification behaviour

- One event, at most one notification. Health flapping is handled before the event exists: a state changes only after consecutive confirmations ([health](./processes/servers/server-health.md)).
- Messages are short: an emoji for the state, the subject's name, one sentence, and an **Open in Proxier** button to the subject's page.
- Messages are in the admin's language. Times use the configured time zone.
- Delivery details (retries, Telegram rate limits, failures): [notifications process](./processes/platform/notifications.md).

Subscriptions' texts (EN; each has its RU twin). The dates are the last day the link works, in the admin's language and time zone; the button opens the link or subscription page.

| Event | Emoji | Title | Sentence |
|---|---|---|---|
| `link.expiring_soon` | ⏳ | {subject} expires on {date} | Extend it on its page if it should keep working. |
| `link.expired` | ⏳ | {subject} expired on {date} | Its app gets the “expired” entry on its next refresh. |
| `link.shared_suspected` | 🟡 | {subject} looks shared | Fetched from {networks} and {apps} in 24 h ("6 networks", "1 app": the language's plural forms). |
| `subscription.all_unhealthy` | 🟡 | Every server of {subject} is unhealthy | Hiding them would leave nothing, so all {count} are served. |
