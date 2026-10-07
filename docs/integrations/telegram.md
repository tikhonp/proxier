# Telegram (notification channel)

The first notification channel. The bot only sends. Commands are in the backlog.

## Setup

1. Create a bot with @BotFather and copy its token.
2. Settings → Integrations → Telegram: paste the token. Proxier calls `getMe` and shows the bot's username.
3. Send `/start` to the bot from the account (or group) that should receive notifications, then click **Detect chat**. Proxier calls `getUpdates`, lists the chats it finds, and stores the chosen chat ID. A chat ID can also be typed.
4. **Send test**.

`getUpdates` only works while the bot has no webhook. Proxier never sets one.

## Sending

- `sendMessage` with `chat_id`, `parse_mode: HTML` (every interpolated value is HTML-escaped), `disable_web_page_preview: true`, and an inline keyboard with one URL button, **Open in Proxier**, linking to the subject's page.
- At most one message per second.
- On `429`, Proxier waits the returned `retry_after` before trying again. Other errors are retried 3 times (10 s, 1 min, 5 min) ([notifications](../processes/platform/notifications.md)).

## Message shape

```
🔴 de-1 is down
Unreachable over SSH and from 3/3 nodes abroad.
[Open in Proxier]
```

| State or kind | Emoji |
|---|---|
| healthy / recovered / ready | 🟢 |
| degraded | 🟡 |
| blocked | 🟣 |
| down / failed | 🔴 |
| security (new IP sign-in, host key changed, lockout) | 🔐 |
| routing digest / info | 🧭 |
| link expiring / expired / shared | 🔗 |

## Reachability

Proxier sends from home. If `api.telegram.org` becomes unreachable from Russia, the home router's routing list can carry Telegram's domains like any other service, and Proxier's requests then go through the router's tunnel like the rest of the LAN. This is allowed: only traffic to Proxier's own servers must stay out of the tunnel ([architecture](../architecture.md#runtime-shape)).
