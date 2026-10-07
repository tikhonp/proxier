-- +goose Up

-- Settings: one row per value that differs from its default. Secret values
-- are sealed by the vault under "setting:<key>" and kept in secret_value.
CREATE TABLE settings (
    key          TEXT PRIMARY KEY,
    value        TEXT,
    secret_value BLOB,
    updated_at   TEXT NOT NULL,
    CHECK ((value IS NULL) <> (secret_value IS NULL))
) STRICT;

-- Events: the append-only activity history. Recorded in the transaction of
-- the change they describe; never updated or deleted except by retention.
CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    time         TEXT NOT NULL,
    module       TEXT NOT NULL,
    type         TEXT NOT NULL,
    subject_type TEXT NOT NULL DEFAULT '',
    subject_id   TEXT NOT NULL DEFAULT '',
    actor        TEXT NOT NULL,
    payload      TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX events_time ON events (time);
CREATE INDEX events_subject ON events (subject_type, subject_id, id);
CREATE INDEX events_type ON events (type, id);

-- +goose Down
DROP TABLE events;
DROP TABLE settings;
