-- +goose Up

-- The chats the bot heard from lately, newest kept, for Detect chat: the
-- bot confirms every update it reads, so Telegram no longer holds them.
CREATE TABLE telegram_chats (
    chat_id  INTEGER PRIMARY KEY,
    type     TEXT NOT NULL CHECK (type IN ('private', 'group', 'supergroup', 'channel')),
    title    TEXT NOT NULL CHECK (length(title) <= 256),
    username TEXT NOT NULL DEFAULT '' CHECK (length(username) <= 64),
    heard_at TEXT NOT NULL
) STRICT;
CREATE INDEX telegram_chats_heard ON telegram_chats (heard_at);

-- +goose Down
DROP TABLE telegram_chats;
