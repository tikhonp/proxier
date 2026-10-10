# Platform

The platform is everything the modules stand on. It has no business logic of its own about proxies, but it owns how Proxier signs in, stores secrets, runs work, remembers history, talks to the admin, and reaches remote machines. Modules use it through the registrations described in [architecture](../architecture.md#platform-and-modules).

## Admin and sign-in

- There is exactly **one admin**. It is created with `proxier manage create-admin` (username + password, asked interactively), and no other way. `proxier manage reset-password` sets a new password from the container's shell, and ends every session.
- Passwords are at least 12 characters and stored as an argon2id hash.
- Sign-in is username + password on `/login`. There is no 2FA in the first version. The flow is built so a second step can be inserted between "password correct" and "session created".
- **Sessions**: an HttpOnly, Secure, SameSite=Lax cookie. They expire after 7 days idle and 30 days total. Every session lasts the same; there is no "remember me". Settings → Security lists the sessions (created, last seen, IP, browser) with **Sign out** for each and **Sign out everywhere**.
- **Lockout**: 5 failed sign-ins from one IP within 15 minutes lock that IP out for 15 minutes. The response is the same for a wrong username and a wrong password.
- A sign-in from an IP not seen in the last 30 days sends a notification. With password-only sign-in on a public host, this is the main alarm for a stolen password.
- Every form that changes something carries a CSRF token. `next=` redirects accept only local paths.

Flow and edge cases: [sign-in](../processes/platform/sign-in.md).

## Settings

Settings are grouped by module. Each module declares its own section, fields, defaults and validation. Secret settings are stored encrypted. They are shown masked, with **Reveal** and **Replace** actions, and only "changed" is recorded in the event (never the value).

| Section | Contents |
|---|---|
| General | Instance name, public base URL (read-only, from env), display time zone, default language (the admin's language when the account is created), admin contact shown in stub entries (e.g. `@tikhonp`); then **Language**, the admin's own language. |
| Security | Change password, sessions, lockout parameters (`security.lockout_failures` 5, `security.lockout_window` 15m, `security.lockout_duration` 15m). |
| SSH | Proxier's public key (copy; **Regenerate** with a warning that every server, jump host and router must get the new key), your personal public keys installed on new servers, known hosts (fingerprints, **Forget**, **Accept new key** when one changed). |
| Integrations | Cloudflare (API token, zones Proxier may use, test), Telegram (bot token, chat, **Detect chat**, **Send test**), check-host.net (on/off, nodes in Russia, nodes abroad), tailnet (state, node name, IP, **Re-authenticate**), Chromium (CDP URL from env, state), GitHub token (optional, raises API limits). |
| Notifications | One toggle per event type, grouped by module ([events](../events.md)). |
| Servers | Hostname pattern, locations, proxy test URL (default: Cloudflare's speed endpoint, 256 KB), check intervals and thresholds ([health](../processes/servers/server-health.md)), stats retention. |
| Subscriptions | Default format, default update interval, expiry warning days, shared-link alert thresholds, tombstone period. |
| Routing | Daily refresh time, safety thresholds, drift-check interval, auto-repair, default Shadowrocket rule policy. |
| Retention | Jobs, check results, metrics, fetches, discovery runs. |
| Backups | Schedule, how many to keep, **Download latest**, **Back up now**. |
| About | Version, uptime, database size, tailnet and Chromium state. |

## Secrets

- One **master key** from the environment encrypts every secret with AES-256-GCM: generated values, link and config tokens, deployed files that contain secrets, setting secrets, Proxier's SSH private key ([ADR 0012](../adr/0012-secrets-encrypted-root-password-never-stored.md)).
- The **root password** of a new VPS exists only inside its provisioning job, encrypted, and is erased as soon as Proxier's key login works. It is never logged, never shown again, and never written into an event.
- Lookups by secret use an HMAC of the value under a key derived from the master key, so a token is never stored in plain text or compared in plain text.
- **Redaction**: every job registers the secrets it handles, and log lines are scrubbed before they are stored. Values the UI must show (a link URL, a connection URI) appear on the object's own page, where the admin opens them deliberately.

## SSH

- Proxier has **one ed25519 key pair**, generated on first start. It is installed on every server at provisioning, and installed by the admin on jump hosts and routers.
- Your **personal public keys** (Settings → SSH) are added to every new server alongside Proxier's, so you can always log in yourself. They are one settings field: one `authorized_keys` line per line, each checked when saved (a bad line is named by its number).
- **Host keys are pinned on first contact** (trust on first use), per address, one key type each (the client offers only that type, so a server with several host keys never looks changed). Two first-contact modes: servers are pinned when the handshake succeeds (nobody could have confirmed a fingerprint at provisioning), while routers and jump hosts stop with the fingerprint, which the admin confirms before it is pinned. When a pinned key changes, every job touching that host fails with "host key changed", a notification is sent once, and the known host shows the old and new fingerprints with **Accept new key** or **Forget**. Nothing reconnects until one is clicked ([ADR 0007](../adr/0007-agentless-ssh-with-pinned-host-keys.md)). Retiring a server forgets its pins, so a provider reusing the address is a first contact.
- **Regenerate** replaces Proxier's key (type `regenerate` to confirm) and notifies; every server, jump host and router must be given the new one.
- **Jump hosts**: a connection to a router can go through one jump host. Both hops use Proxier's key, and both host keys are pinned. Each hop has its own first-contact rule (`Target.FirstContact` for the last hop, `Target.JumpFirstContact` for the jump host; the zero value is pin-on-first-contact, so a caller sets both). An awaiting router's probe pins the router on first contact but refuses an unknown jump host.
- Commands run with timeouts. Output is streamed into the job log (redacted).

## Tailnet

Proxier joins the headscale tailnet as its own node (`proxier`) through an embedded tailnet client. It does not use blackberry's host network. It uses the tailnet only for outgoing connections to jump hosts and routers, and serves nothing on it ([ADR 0008](../adr/0008-tsnet-node-for-router-reachability.md)). Settings → Integrations shows its state, tailnet IP and key expiry. Without `PROXIER_TS_AUTHKEY` the tailnet is off, and routers must be reachable directly from blackberry's network. The node's state lives in `/data/tailnet` (not in the database or its backups), so a restart needs no key again; after restoring a backup on a new volume, Settings → Integrations → Tailnet takes a new pre-auth key.

## Jobs and scheduler

Queues, states, steps, keys, retries, cancellation and logs are described in [architecture](../architecture.md#jobs). The UI:

- **Jobs page**: every job, newest first, filterable by queue, state, type and subject. Running jobs show their current step. Failed jobs show the error line.
- **Job page**: steps with state and duration, the live log (follow mode, copy, download), payload summary (redacted), attempts, **Cancel** and **Retry**. The subject (server, router…) is linked.
- **Header indicator**: the number of running jobs, opening a list of the running ones with their progress. Failed jobs since the last visit are marked.
- Objects (server, router, discovery run) embed their latest job's progress, so the admin rarely needs the Jobs page itself.

Default schedules:

| Schedule | Default | Queue |
|---|---|---|
| Self-check + stats per active server | every 5 min, jittered | checks |
| Proxy test per endpoint | every 5 min, offset from the self-check | checks |
| External check per server | every 30 min, and on demand | checks |
| Reference check | every 1 min while any server is checked | checks |
| Upstream refresh | daily 04:00 | refresh |
| Catalog refresh + reverse index | daily 04:30 | refresh |
| Drift check per router | every 6 h | routers |
| Link expiry scan | every 15 min | maintenance |
| Shared-link scan | every 15 min | maintenance |
| Backup | daily 03:30 | maintenance |
| Retention cleanup | daily 05:00 | maintenance |

Flow and edge cases: [jobs](../processes/platform/jobs.md).

## Events and Activity

Events are described in [architecture](../architecture.md#events-and-history) and catalogued in [events.md](../events.md). The **Activity page** shows them newest first. Filters: module, event type, subject, actor, time range. Each row reads as a sentence ("Admin rotated credentials of nl-1") with a link to the subject. Each object page has an **Activity** tab with the same list filtered to it.

## Notifications

Telegram is the only channel in the first version. Channels are an extension point. Setup: a bot token from BotFather, then **Detect chat** (you send `/start` to the bot, and Proxier reads the chat ID from the bot's updates), then **Send test**. Event types are toggled in Settings → Notifications. Flow and edge cases: [notifications](../processes/platform/notifications.md).

## Languages and time

- The UI is in **English and Russian** from the first version. Every UI string, notification text and stub entry text goes through message catalogs. Modules ship their own EN and RU catalogs.
- The admin's language is changed only in Settings → General → **Language** (`admin.language_changed`); the admin menu and the `:` pop-up have no switch. The sign-in page follows the browser's `Accept-Language`.
- Each **link has a language** (default from Settings), used for its stub entry text.
- Telegram messages use the admin's language.
- Times are shown in the display time zone. A relative form ("3 min ago") is used, with the absolute time on hover. Stored times are UTC.
- The design must allow Russian strings about 30 % longer than English.

## Backups

See [deployment](../deployment.md#backups). Backup failures notify.

## Platform events

`admin.created`, `auth.*`, `settings.changed`, `ssh.host_key_changed`, `ssh.host_key_accepted`, `job.failed`, `backup.*`: see [events](../events.md#platform).
