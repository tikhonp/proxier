# Telegram (notification channel and bot)

The notification channel, and the bot members give commands to in their own chat ([telegram bot](../processes/platform/telegram-bot.md)).

## Setup

One bot serves every member; each member has their own chat.

1. Create a bot with @BotFather and copy its token.
2. Settings → Integrations → Telegram (`settings: manage`): paste the token. Proxier calls `getMe` and shows the bot's username.
3. Each member, on their own Settings → Notifications: send `/start` to the bot from the account (or group) that should receive their notifications, then click **Detect chat**. Proxier lists the chats the bot heard from (kept by the poller, below) except those other members already have, and stores the chosen chat ID as the member's. A chat ID can also be typed.
4. **Send test**, to the member's chat.

## Receiving

- `getUpdates` long polling (`timeout` 25 s), with `offset` one past the last update read, which confirms it: Telegram then drops it. `allowed_updates`: `message`, `channel_post`, `my_chat_member`, `callback_query`. It only works while the bot has no webhook; Proxier never sets one. Two Proxiers polling one bot get `409 Conflict`; each logs it and tries again after 10 s.
- Every update's chat goes into `telegram_chats` (the newest 100) for **Detect chat**.
- Messages and button taps in private chats are answered ([telegram bot](../processes/platform/telegram-bot.md)): `sendMessage` (HTML, escaped, with callback buttons whose `callback_data` is `c<conversation>.<choice>`), `editMessageText` for the answer to a tap, `answerCallbackQuery` for every tap.
- `setMyCommands` on the first poll for a token: the default menu in English, and one for `language_code` `ru`.

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
