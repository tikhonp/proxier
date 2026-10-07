-- +goose Up

-- The admin's choices that differ from the catalog default.
-- Per event type: one channel (Telegram). A second channel adds a column to the key.
CREATE TABLE notification_rules (
    event_type TEXT PRIMARY KEY,
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    updated_at TEXT NOT NULL
) STRICT;

-- One notification per event and channel, rendered when queued.
CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id   INTEGER NOT NULL,                -- no foreign key: events can be pruned
    channel    TEXT NOT NULL,                   -- "telegram"
    lang       TEXT NOT NULL CHECK (lang IN ('en', 'ru')),
    message    TEXT NOT NULL,                   -- JSON notify.Message, plain text (escaped by the channel)
    state      TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,      -- counted tries (429 waits are not)
    last_error TEXT NOT NULL DEFAULT '',
    job_id     INTEGER,
    created_at TEXT NOT NULL,
    sent_at    TEXT,
    UNIQUE (event_id, channel)
) STRICT;
CREATE INDEX notifications_state ON notifications (state, created_at);
-- Retention deletes by age whatever the state.
CREATE INDEX notifications_created ON notifications (created_at);

-- +goose Down
DROP TABLE notifications;
DROP TABLE notification_rules;
