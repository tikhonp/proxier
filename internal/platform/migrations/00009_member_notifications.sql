-- +goose Up

-- Notifications are per member: each has their own Telegram chat and their
-- own rules, and a notification is one recipient's. The one chat and the
-- rules there were are member 1's: they were the admin's.
ALTER TABLE members ADD COLUMN telegram_chat_id TEXT NOT NULL DEFAULT '' CHECK (length(telegram_chat_id) <= 64);
ALTER TABLE members ADD COLUMN telegram_chat_title TEXT NOT NULL DEFAULT '' CHECK (length(telegram_chat_title) <= 128);
UPDATE members SET
    telegram_chat_id = COALESCE((SELECT value FROM settings WHERE key = 'telegram.chat_id'), ''),
    telegram_chat_title = COALESCE((SELECT value FROM settings WHERE key = 'telegram.chat_title'), '')
WHERE id = 1;
DELETE FROM settings WHERE key IN ('telegram.chat_id', 'telegram.chat_title');

-- A member's choices that differ from the catalog default.
CREATE TABLE notification_rules_new (
    member_id  INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (member_id, event_type)
) STRICT;
INSERT INTO notification_rules_new (member_id, event_type, enabled, updated_at)
    SELECT 1, event_type, enabled, updated_at FROM notification_rules WHERE EXISTS (SELECT 1 FROM members WHERE id = 1);
DROP TABLE notification_rules;
ALTER TABLE notification_rules_new RENAME TO notification_rules;

-- One notification per event, channel and recipient. The member has no
-- foreign key: like the event, the notification is history.
CREATE TABLE notifications_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id   INTEGER NOT NULL,                -- no foreign key: events can be pruned
    channel    TEXT NOT NULL,                   -- "telegram"
    member_id  INTEGER NOT NULL,                -- the recipient
    lang       TEXT NOT NULL CHECK (lang IN ('en', 'ru')),
    message    TEXT NOT NULL,                   -- JSON notify.Message, plain text (escaped by the channel)
    state      TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,      -- counted tries (429 waits are not)
    last_error TEXT NOT NULL DEFAULT '',
    job_id     INTEGER,
    created_at TEXT NOT NULL,
    sent_at    TEXT,
    UNIQUE (event_id, channel, member_id)
) STRICT;
INSERT INTO notifications_new (id, event_id, channel, member_id, lang, message, state, attempts, last_error, job_id, created_at, sent_at)
    SELECT id, event_id, channel, 1, lang, message, state, attempts, last_error, job_id, created_at, sent_at FROM notifications;
-- IDs are never reused, even when retention left no row to copy.
DELETE FROM sqlite_sequence WHERE name = 'notifications_new';
INSERT INTO sqlite_sequence (name, seq) SELECT 'notifications_new', seq FROM sqlite_sequence WHERE name = 'notifications';
DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;
CREATE INDEX notifications_state ON notifications (state, created_at);
-- Retention deletes by age whatever the state.
CREATE INDEX notifications_created ON notifications (created_at);
CREATE INDEX notifications_member ON notifications (member_id, state, created_at);

-- +goose Down
-- Back to one chat: member 1's, with member 1's rules and notifications.
INSERT INTO settings (key, value, updated_at)
    SELECT 'telegram.chat_id', telegram_chat_id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now') FROM members WHERE id = 1 AND telegram_chat_id != '';
INSERT INTO settings (key, value, updated_at)
    SELECT 'telegram.chat_title', telegram_chat_title, strftime('%Y-%m-%dT%H:%M:%fZ', 'now') FROM members WHERE id = 1 AND telegram_chat_title != '';
ALTER TABLE members DROP COLUMN telegram_chat_title;
ALTER TABLE members DROP COLUMN telegram_chat_id;

CREATE TABLE notification_rules_old (
    event_type TEXT PRIMARY KEY,
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    updated_at TEXT NOT NULL
) STRICT;
INSERT INTO notification_rules_old (event_type, enabled, updated_at)
    SELECT event_type, enabled, updated_at FROM notification_rules WHERE member_id = 1;
DROP TABLE notification_rules;
ALTER TABLE notification_rules_old RENAME TO notification_rules;

CREATE TABLE notifications_old (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id   INTEGER NOT NULL,
    channel    TEXT NOT NULL,
    lang       TEXT NOT NULL CHECK (lang IN ('en', 'ru')),
    message    TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    job_id     INTEGER,
    created_at TEXT NOT NULL,
    sent_at    TEXT,
    UNIQUE (event_id, channel)
) STRICT;
INSERT INTO notifications_old (id, event_id, channel, lang, message, state, attempts, last_error, job_id, created_at, sent_at)
    SELECT id, event_id, channel, lang, message, state, attempts, last_error, job_id, created_at, sent_at FROM notifications WHERE member_id = 1;
DELETE FROM sqlite_sequence WHERE name = 'notifications_old';
INSERT INTO sqlite_sequence (name, seq) SELECT 'notifications_old', seq FROM sqlite_sequence WHERE name = 'notifications';
DROP TABLE notifications;
ALTER TABLE notifications_old RENAME TO notifications;
CREATE INDEX notifications_state ON notifications (state, created_at);
CREATE INDEX notifications_created ON notifications (created_at);
