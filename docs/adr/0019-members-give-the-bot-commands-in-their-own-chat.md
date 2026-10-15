# Members give the bot commands in their own chat, and a new link's URL goes there

The Telegram bot only sent notifications. A member who wanted a link for someone had to open Proxier, find **New link**, fill the form and copy the URL into a messenger, which is where the URL was going anyway. Now a member writes `/link` to the bot, sends the link's name, taps a subscription, and gets the URL back in the same chat ([telegram bot](../processes/platform/telegram-bot.md), [link lifecycle](../processes/subscriptions/link-lifecycle.md#steps-from-telegram)).

## Who is talking: the private chat the member chose

The bot knows a member by the chat they picked on Settings → Notifications, and only when it is a **private** chat. Telegram sets a private chat's ID to its user's ID, so a message there is from that Telegram account and no one else. In a group, anyone in the group writes, so the bot answers nothing in groups, even the member's own notification group. Choosing the chat needed a signed-in session (and the second step, if the member has one), so the chat is a way in that the member set up themselves. A removed member's chat, or one of a member who hasn't finished setup, is nobody's.

The bot is a way in without a password or a second step on each command. The commands are kept to what is safe at that strength: each declares an area and a level, the member's grant is read again on every message, and everything runs as `member:<id>` with the member's scope, as a page would. Nothing a command does goes past what the member could do on the page. Commands that delete, rotate or change access stay out of the bot.

## The URL goes to Telegram

A link's token used to appear only on its own page. Now `/link` sends the URL in the bot's answer, in the member's private chat. That is the point of the command: the member forwards it, or copies it from there. Telegram keeps the message on its servers, and anyone who later gets into the member's Telegram account can read it. That is the same exposure as the member pasting the URL into a Telegram chat themselves, which is what they did before. The URL is sent once, in the answer to the member who asked; it is never in an event, a notification, a log line or a job payload, and the answer is never logged.

## Long polling, not a webhook

Proxier reads updates with `getUpdates` and a 25-second long poll. A webhook would need a public route for Telegram, and Telegram's addresses reaching home through the gateway; polling needs only the outbound HTTPS Proxier already uses for notifications. Polling confirms each update it reads, so Telegram no longer holds them for **Detect chat**: the bot keeps the chats it heard from (the newest 100) in `telegram_chats`, and Detect chat reads them there.

## Conversations live in memory

A conversation (the question asked, the buttons offered) is kept in memory per chat for 15 minutes. A restart forgets it, and the member starts again. A table would survive restarts, but a half-made link is worth less than the schema and the cleanup.
