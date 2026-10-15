# Telegram bot

**Actors**: a member, in the private Telegram chat they chose for their notifications; the bot (the platform's poller); the module that declares the command; Telegram Bot API.

The bot that sends notifications also answers members. A member writes a command in their own chat, and the bot runs it as that member, with their levels, in their language. Each command is a short conversation: the bot may ask for a text or offer buttons, and the member's answer goes to the command's next step. The decision and its trade-offs: [ADR 0019](../../adr/0019-members-give-the-bot-commands-in-their-own-chat.md).

Commands:

| Command | Module | Needs | What it does |
|---|---|---|---|
| `/link` | subscriptions | `subscriptions: own` | A new link: a name, then a subscription, then the URL ([link lifecycle](../subscriptions/link-lifecycle.md#steps-from-telegram)) |
| `/help`, `/start` | platform | — | The commands the member's levels reach |
| `/cancel` | platform | — | Ends the conversation |

## Steps

1. With a bot token set, Proxier long-polls the bot (`getUpdates`, 25 s), confirming every update it reads. Without a token it checks again every 30 s. When Telegram fails, the error is logged and the poll tries again after 10 s.
2. On its first poll for a token, Proxier sets the bot's command menu, in English and in Russian (for a Telegram in Russian).
3. Every update's chat is kept as heard (type, title, @name, time), the newest 100, for **Detect chat** ([notifications](./notifications.md)). This is bookkeeping, not an event.
4. A message or a button tap in a private chat is answered. The bot finds the member whose notification chat it is: in, set up, not removed. With none, it says how to choose the chat in Proxier, in the language of the person's Telegram.
5. A command runs if the member's grant reaches its area and level; otherwise "Your access in Proxier doesn't allow this." A new command ends the conversation before it.
6. A text that isn't a command goes to the step that asked for it. With nothing asked, the bot answers with the help.
7. A tap on a button runs that button's step. The tapped message is replaced by the answer, so its buttons can't be tapped again. A button of a conversation that ended answers "This has expired. Start again." and runs nothing.
8. Each step records its own events, as the member (`member:<id>`), exactly as the page doing the same would.

## Rules

- Only a private chat is answered: in a group anyone writes, so the bot never answers there, even in a member's own notification group.
- The member is found again on every message, with their grant at that moment: a level lowered, a chat changed or a member removed applies to the next message.
- A conversation is in memory: it ends after 15 minutes, on `/cancel`, with a new command, or when Proxier restarts.
- Answers are in the member's language. To someone who isn't a member, in their Telegram's language (Russian or English).
- A step that fails ends the conversation with "Something went wrong. Try again, or do it in Proxier."; the error is logged.
- Answers are never logged: `/link`'s holds a link's URL.
- A module adds a command with `module.BotCommandDeclarer` (`bot.Command`: name, area, level, help, the first step).

## Edge cases (each is a test)

- A command, a text asked for, a button: each step runs as the member, and the tap's answer replaces the tapped message (`TestConversationRunsAsTheMember`).
- The same button tapped twice → the second tap runs nothing and says it expired (`TestConversationRunsAsTheMember`).
- `/echo@bot hi`: the bot's name is dropped and the rest is the command's text (`TestConversationRunsAsTheMember`).
- Updates read are confirmed: a second poll answers nothing again (`TestPollConfirmsWhatItRead`).
- The command menu is set once per token, in English and Russian (`TestCommandMenuIsSetOncePerBot`).
- A chat that is no member's → told how to choose it, in its Telegram's language, and kept for Detect chat (`TestUnknownChatIsToldHowToBecomeOne`).
- A group, even a member's notification chat → never answered, still kept for Detect chat (`TestGroupChatIsNeverAnswered`).
- `/help` lists only what the member's grant reaches; a command beyond it is refused; a member who reaches none is told the chat is for notifications (`TestCommandsFollowTheGrant`).
- Answers in the member's language (`TestRepliesAreInTheMembersLanguage`).
- A removed member's chat → no longer theirs (`TestRemovedMembersChatIsNoLongerTheirs`).
- `/cancel`, a new command, or 15 minutes → the conversation's buttons and question are gone (`TestConversationEndsOnCancelNewCommandOrExpiry`).
- A step that fails → the apology; an unknown command → the help (`TestFailingCommandApologises`).
- No bot token → no call to Telegram (`TestNoTokenNoCalls`).
- 101 chats heard → the newest 100 kept (`TestDetectChatKeepsTheNewestChats`).
