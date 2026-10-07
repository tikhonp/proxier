-- +goose Up

-- The one admin. Created only by `proxier manage create-admin`.
CREATE TABLE admin (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    username            TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64),
    password_hash       TEXT NOT NULL,   -- PHC string, argon2id
    language            TEXT NOT NULL CHECK (language IN ('en', 'ru')),
    created_at          TEXT NOT NULL,
    password_changed_at TEXT NOT NULL
) STRICT;

-- Sessions. The cookie holds a token; the row holds vault.Lookup(token).
-- Idle expiry is last_seen_at + 7 d; expires_at is the absolute one.
CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   BLOB NOT NULL UNIQUE,
    admin_id     INTEGER NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    created_ip   TEXT NOT NULL,
    last_ip      TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',   -- truncated to 512 chars
    ended_at     TEXT,
    end_reason   TEXT CHECK (end_reason IN ('signed_out', 'signed_out_everywhere', 'password_changed', 'password_reset')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
CREATE INDEX sessions_open ON sessions (admin_id) WHERE ended_at IS NULL;
-- Retention (0c) removes sessions ended or expired more than 30 days ago.
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);

-- Every sign-in attempt that was checked (not those refused by a lockout).
-- IP only: no username is stored.
-- Drives the lockout and new_ip. Kept 30 days.
CREATE TABLE sign_in_attempts (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ip      TEXT NOT NULL,
    time    TEXT NOT NULL,
    success INTEGER NOT NULL CHECK (success IN (0, 1))
) STRICT;
CREATE INDEX sign_in_attempts_ip ON sign_in_attempts (ip, time);
CREATE INDEX sign_in_attempts_time ON sign_in_attempts (time);

-- +goose Down
DROP TABLE sign_in_attempts;
DROP TABLE sessions;
DROP TABLE admin;
