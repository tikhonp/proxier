# Notifications

**Actors**: Admin; the notifier (an event subscriber); Telegram Bot API.

Notifications tell the admin about events that need attention: a server down or blocked, a failed setup, a sync failing, a link that looks shared, a sign-in from a new IP. Telegram is the only channel in the first version. Channels are an extension point.

## Steps — setup

1. Settings → Integrations → Telegram: the admin pastes a bot token from BotFather. Proxier calls `getMe` and shows the bot's name. A wrong token is refused on the spot.
2. **Detect chat**: the admin sends `/start` to the bot. Proxier reads the bot's recent updates, shows the chats found (name, type), and the admin picks one. The admin can also type a chat ID.
3. **Send test**: a test message goes out. Its result (sent, or the error) is shown. → `settings.changed{keys: telegram}`
4. Settings → Notifications: one toggle per event type, grouped by module, initially the defaults from [events](../../events.md).

## Steps — delivery

1. The notifier receives every committed event (at least once). For an event type whose rule is on, it renders the message in the admin's language and queues a `notify` job. Events whose type is off produce nothing.
2. The job sends the message: an emoji for the state, the subject's name, one sentence, and a button **Open in Proxier** linking to the subject's page.
3. Telegram answers `429` → the job waits `retry_after` and tries again (not counted as a failed attempt).
4. Other errors → retried after 10 s, 1 min, 5 min. After the last one, the notification is `failed` with the error, visible in Activity and on the dashboard.
5. Sent → the notification is `sent`, with the time.

## Rules

- One event, at most one notification. Duplicate delivery of an event is recognised by the event ID and doesn't send twice.
- Nothing is sent from inside a transaction. A state change commits first, then the notification is queued.
- Messages go out one at a time, at most one per second, which keeps within Telegram's per-chat limits. A burst queues; it is never dropped.
- Telegram being unreachable never blocks anything else. Notifications wait in their queue.
- Messages never contain secrets: no tokens, no connection URIs, no IPs of link holders beyond what the event needs (the shared-link alert shows counts, not IPs).
- Texts are rendered when queued, so they describe that moment even if a name changes before delivery.
- If no Telegram chat is configured, events are still recorded. The dashboard shows "Notifications are not configured".

## Edge cases (each is a test)

- An invalid bot token → refused at setup, nothing saved.
- **Detect chat** before `/start` was sent → "No chats yet. Send /start to @bot and try again".
- Send test with a chat that blocked the bot → the error is shown, and the setting is saved anyway.
- `server.health_changed` to `down` with its rule on → one message with an **Open in Proxier** button to the server page.
- The same event delivered twice to the notifier (restart between delivery and cursor update) → one message.
- `server.health_changed` to `degraded` (rule off by default) → no message, but the event is in Activity.
- 30 events in one second → 30 messages over about 30 seconds, in order.
- Telegram answers `429 retry_after=20` → the message waits 20 s, then is sent. The attempt isn't counted.
- Telegram down for 10 minutes → the message fails after its last retry. It shows as failed on the dashboard, and later messages still try.
- The admin's language is Russian → the message is Russian. The server name is not translated.
- A link renamed after its shared-link alert was queued → the message shows the name at the time of the event.
