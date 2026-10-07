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
| `template.archived` | template | — | off | |
| `template.unarchived` | template | — | off | |
| `template.deleted` | template | name | off | A template that never built a server. |
| `template.agent_session_opened` | template | expires_at, context | **on** | "An agent can edit the nginx-xhttp draft until 18:41". Anyone holding the copied prompt can do so. |
| `template.draft_saved` | template | by (admin / agent), files (the changed paths, at most 50), changed (their number), from_version (when the draft was created from a version) | off | A save that changes no file records nothing. |
| `template.draft_validated` | template | by, errors, warnings | off | |
| `template.agent_session_closed` | template | reason (expired / revoked / published / discarded), saves | off | |
| `location.created` | location | code, name, country | off | |
| `location.changed` | location | fields (`name`, `country`) | off | Only when something changed. |
| `location.deleted` | location | code, name | off | Only a location without servers. |
| `server.created` | server | IP, template version | off | |
| `server.provisioning_failed` | server | step, error | **on** | "nl-2: setup failed at 'Issue certificate': …" |
| `server.activated` | server | proxy test timings, forced | **on** | "nl-2 is ready (🇳🇱 Netherlands 2)"; when forced by **Activate anyway**: "nl-2 is active without a passing proxy test" |
| `server.redeployed` | server | kind, from version, to version, files changed | off | |
| `server.redeploy_failed` | server | kind, step, error | **on** | |
| `server.credentials_rotated` | server | keys rotated | **on** | |
| `server.health_changed` | server | from, to, reason, check summary | **on** for changes to `blocked`, `down`, and back to `healthy`. **off** for `degraded` and `unknown` | "🔴 de-1 is down: unreachable over SSH and from 3/3 nodes abroad" |
| `server.still_unhealthy` | server | state, since | **on** | Reminder every 24 h while blocked or down. |
| `server.cert_expiring` | server | days_left | **on** | Once a day while fewer than 14 days are left. |
| `server.disk_low` | server | free_pct | **on** | Under 10 % free. |
| `server.checks_paused` | server | until | off | |
| `server.checks_resumed` | server | — | off | |
| `server.retired` | server | DNS removed, stack removed | off | |
| `server.notes_changed` | server | — | off | Only when the notes changed. |
| `server.rollout_started` | server | servers, to_version | off | A rolling upgrade began. |
| `server.rollout_finished` | server | state, done, failed, not_started | off | |
| `health.home_offline` | — | reference check results | off | Telegram is unreachable anyway. It's shown on the dashboard. |
| `health.foreign_unreachable` | — | reference check results | **on** | "Foreign internet is unreachable from home; server verdicts are on hold" |
| `health.home_recovered` | — | duration | **on** | |

## Subscriptions

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `subscription.created` / `.updated` / `.deleted` | subscription | changes | off | |
| `subscription.servers_changed` | subscription | added, removed, reordered | off | Includes removals caused by retirement. |
| `subscription.all_unhealthy` | subscription | servers | **on** | Hiding would leave the output empty, so every server is served ([fetch](./processes/subscriptions/subscription-fetch.md)). |
| `link.created` | link | subscription, expiry | off | |
| `link.disabled` / `link.enabled` | link | — | off | |
| `link.token_regenerated` | link | — | off | |
| `link.subscription_changed` | link | from, to | off | |
| `link.expiry_changed` | link | from, to | off | |
| `link.expiring_soon` | link | expiry | **on** | Once, 3 days before (setting). |
| `link.expired` | link | — | **on** | |
| `link.deleted` | link | — | off | |
| `link.cut_off` | link | servers rotated | off | The rotations record their own events. |
| `link.shared_suspected` | link | networks, apps, window | **on** | "Link 'Alex' was fetched from 6 networks and 3 apps in 24 h" |

## Routing

| Event | Subject | Payload | Notify | Notes |
|---|---|---|---|---|
| `routing.catalog_refreshed` | — | entries, per source | off | |
| `routing.catalog_refresh_failed` | — | source, error, failing since | **on** after 3 days of failures | |
| `routing.service_added` / `.removed` / `.updated` | service | selector, tag | off | |
| `routing.snapshot_accepted` | service | added, removed, counts | **on, as one daily digest** | "Routing refresh: 3 services changed (+42 / −5 domains), 1 rejected" |
| `routing.snapshot_rejected` | service | reason, previous and new counts | **on** (in the digest, and alone when outside the daily refresh) | |
| `routing.refresh_failing` | service | consecutive failures, error | **on** at 3 | |
| `routing.list_created` / `.updated` / `.deleted` | routing list | changes | off | |
| `routing.list_refused_server_hostname` | routing list | domain, server | off | Shown in the UI at once. |
| `routing.router_added` / `.removed` | router | — | off | |
| `routing.router_connected` | router | RouterOS version, board | off | First successful connection. Ends "awaiting setup". |
| `routing.router_synced` | router | tags added / updated / removed, counts | off | |
| `routing.router_sync_failed` | router | step, error, consecutive | **on** at the 2nd consecutive failure (at once for a manual sync) | |
| `routing.router_recovered` | router | after failures | **on** | |
| `routing.drift_detected` | router | tags differing | **on** only when auto-repair is off | |
| `routing.unmanaged_tags_found` | router | tags | **on** once per new set | |
| `routing.shadowrocket_created` / `.updated` | Shadowrocket config | — | off | |
| `routing.discovery_completed` / `.failed` | discovery run | hosts found, suggestions | off | The admin is watching the run. |

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
