# Notifications

**Actors**: a member with `settings: manage` (the bot); every member (their own chat and rules); the notifier (an event subscriber); Telegram Bot API.

Notifications tell members about events that need attention: a server down or blocked, a failed setup, a sync failing, a link that looks shared, a sign-in from a new IP. Telegram is the only channel; channels are an extension point. There is one bot, and each member has their own chat and their own rules, so an event goes only to the members it concerns, each in their own language.

## Steps: setup

1. Settings → Integrations → Telegram (`settings: manage`): paste a bot token from BotFather. Proxier calls `getMe` and shows the bot's name. A wrong token is refused on the spot. → `settings.changed{keys: telegram}`
2. Each member, on their own Settings → Notifications: **Detect chat**. The member sends `/start` to the bot; Proxier shows the chats the bot heard from lately (name, type: the newest 100 the [bot](./telegram-bot.md) kept) other than those other members already have, and the member picks one. The member can also type a chat ID, or empty it to get nothing. A private chat chosen here is also where the member gives the bot [commands](./telegram-bot.md).
3. The chat is saved when it is picked or typed, as the member's own. → `member.notifications_changed{keys: telegram.chat}`
4. **Send test** is a separate button: a test message goes to the member's chat and its result (sent, or the error) is shown. The chat stays saved whatever the result.
5. The same page has one toggle per event type the member can be told about, grouped by module, initially the defaults from [events](../../events.md). Each member's toggles are their own. → `member.notifications_changed{keys: notifications.<type>}`

## Recipients

An event goes to every member who is in its **audience**, has finished setup and isn't removed, has the event's type on, and has a chat. The audience of a type (`events.Type.Audience`):

- **By area** (most types): the member who owns the event's subject, and the members who manage the event's area. A subscriptions, routing or router scripts event goes to `manage_all` in that area; a platform or servers event (a server down, a job failed, a host key changed, a backup failed, a lockout) goes to `settings: manage` and `servers: manage`. A member at `own` or `view_all` hears only about their own things.
- **The subject's member only**: a member's own sign-ins and ways in (`auth.signed_in` from a new IP, `auth.password_changed`, the authenticator app, passkeys, `auth.second_factor_reset`, `member.language_changed`, `member.notifications_changed`).
- **Settings managers only**: changes to members (`member.added`, `member.access_changed`, `member.removed`, `member.setup_link_created`, `member.setup_completed`).

The owner of a subject is found when the event is delivered: a member's events name their member (`member:<id>`), and each module whose things have owners answers for its own subjects (`module.SubjectOwner.Owner`: subscriptions and links, routing lists, routers, Shadowrocket configs, custom services, discovery runs, generations). A shared thing (a server, a shared service, a script) and a thing already gone have no owner. A thing handed over to an heir is the heir's.

Settings → Notifications lists only the types the member can be in the audience of: an area they don't reach, or a type for another audience, has no toggle there.

## Steps: delivery

1. The notifier receives every committed event (at least once). It finds the recipients, renders the message in each recipient's language (once per language), and queues one notification and one `notify` job per recipient. An event whose type nobody in its audience has on produces nothing.
2. The job reads the recipient's chat when it runs and sends the message: an emoji for the state, the subject's name, one sentence, and a button **Open in Proxier** linking to the subject's page.
3. Telegram answers `429` → the job waits `retry_after` and tries again (not counted as a failed attempt).
4. Other errors → retried after 10 s, 1 min, 5 min. After the last one, the notification is `failed` with the error, shown on the recipient's dashboard. → `notification.failed{channel, to, error}`
5. Sent → the notification is `sent`, with the time.

## Rules

- One event, at most one notification per recipient. Duplicate delivery of an event is recognised by the event ID and the recipient and doesn't send twice.
- Each recipient's notification is its own job: one chat failing neither holds up nor repeats another's.
- Nothing is sent from inside a transaction. A state change commits first, then the notifications are queued.
- A type may group its notifications (`events.Type.Group`): a recipient's notifications with the same key wait `notify.GroupWindow` (2 min) from the first and go out as one message, titled with their count (`notify.<type>.group.<key>`, else "{n} notifications") and listing each one's text on its own line; its button opens `GroupHref`. A lone one goes out unchanged after the window. Each recipient's group is their own. `server.health_changed` groups by the state reached (blocked, down, healthy again), so a hosting network blocked at once is one "3 servers are blocked", not three messages; its button opens the server list. Retrying a failed group resends the whole group.
- Messages go out one at a time, at most one per second, which keeps within Telegram's per-chat limits. A burst queues; it is never dropped.
- Telegram being unreachable never blocks anything else. Notifications wait in their queue.
- Messages never contain secrets: no tokens, no connection URIs, no IPs of link holders beyond what the event needs (the shared-link alert shows counts, not IPs).
- Texts are rendered when queued, so they describe that moment even if a name changes before delivery. The chat is read when sending, so a chat changed meanwhile applies.
- With no bot, events are still recorded but nothing is queued, and the dashboard tells members with `settings: manage` "Notifications are not configured". A member with no chat gets nothing, and their dashboard says "Your notifications have nowhere to go". Setting either up later does not send the events of the meantime: a backlog of stale alarms would be noise.
- A removed member gets nothing. A notification queued for them before the removal fails when its job runs, without sending.
- Failed notifications, **Retry** and the header's "Telegram failing" are each member's own: a member sees and retries only theirs.
- A failed notification is itself recorded (`notification.failed`, naming the recipient) and never notified about, and it does not record a `job.failed`.

## Edge cases (each is a test)

- An invalid bot token → refused at setup, nothing saved.
- **Detect chat** before `/start` was sent → "No chats yet. Send /start to @bot and try again".
- **Detect chat** with another member's chat among the updates → not offered (`TestDetectChatLeavesOutOtherMembersChats`).
- Send test with a chat that blocked the bot → the error is shown, and the chat is saved anyway.
- Each member chooses their own chat, Send test goes to theirs alone, and an empty chat stops their notifications (`TestEachMemberHasTheirOwnChat`).
- `server.health_changed` to `down` with its rule on → one message with an **Open in Proxier** button to the server page.
- The same event delivered twice to the notifier (restart between delivery and cursor update) → one message.
- `server.health_changed` to `degraded` (rule off by default) → no message, but the event is in Activity.
- Three servers blocked and one down within the window → one "3 servers are blocked" message and the down one by itself, both after the window (`TestBurstOfOneKindIsOneMessage`).
- Two recipients of the same burst → one grouped message each, in each one's language (`TestGroupsArePerRecipient`).
- A grouped message that failed → Retry resends the whole group (`TestFailedGroupIsRetriedWhole`).
- 30 events in one second → 30 messages over about 30 seconds, in order.
- Telegram answers `429 retry_after=20` → the message waits 20 s, then is sent. The attempt isn't counted.
- Telegram down for 10 minutes → the message fails after its last retry. It shows as failed on the dashboard, and later messages still try.
- A notification about a member's link → that member's chat and the chats of the `subscriptions: manage_all` members with the type on, and nobody else: not `view_all`, not `servers: manage`, not a `manage_all` member who turned the type off (`TestLinkNotificationGoesToItsOwnerAndManageAll`, and through the app `TestNotificationReachesTheOwnerAndManageAll`).
- Another member's link → nothing to a member at `own` (`TestOwnLevelHearsNothingOfOthersThings`).
- A member with no chat → nothing queued for them, and the others' messages go out (`TestMemberWithoutChatGetsNothing`).
- Recipients in English and Russian → each message in its recipient's language; the server name is not translated (`TestEachRecipientGetsTheirLanguage`).
- A removed member → nothing new, nothing about them (their things handed over), and what was queued for them fails unsent (`TestRemovedMemberGetsNothing`).
- One recipient's chat blocked → the others get theirs once; the failure, the warning and Retry are that recipient's alone (`TestOneRecipientFailingLeavesTheOthers`).
- A sign-in from a new IP → only to the member who signed in, not to the settings managers (`TestNewIPNoticeGoesOnlyToThatMember`).
- A member added → to the `settings: manage` members, not to a `servers: manage` member, who still gets the servers' events (`TestMemberEventsGoToSettingsManagers`).
- A member's rules → theirs alone; types they can't be told about have no toggle and can't be set (`TestRulesArePerMemberAndFollowTheGrant`, `TestNotificationRulesArePerMember`).
- A link renamed after its shared-link alert was queued → the message shows the name at the time of the event.
