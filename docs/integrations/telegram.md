# Telegram (notification channel)

The notification channel. The bot only sends; commands are in the [backlog](../overview.md#backlog).

## Setup

One bot serves every member; each member has their own chat.

1. Create a bot with @BotFather and copy its token.
2. Settings → Integrations → Telegram (`settings: manage`): paste the token. Proxier calls `getMe` and shows the bot's username.
3. Each member, on their own Settings → Notifications: send `/start` to the bot from the account (or group) that should receive their notifications, then click **Detect chat**. Proxier calls `getUpdates`, lists the chats it finds except those other members already have, and stores the chosen chat ID as the member's. A chat ID can also be typed.
4. **Send test**, to the member's chat.

`getUpdates` only works while the bot has no webhook. Proxier never sets one.

## Sending

- `sendMessage` with `chat_id`, `parse_mode: HTML` (every interpolated value is HTML-escaped), `disable_web_page_preview: true`, and an inline keyboard with one URL button, **Open in Proxier**, linking to the subject's page.
- `chat_id` is the recipient's own chat, read when the message is sent.
- At most one message per second, whatever the chat.
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
