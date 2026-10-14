-- +goose Up

-- Members replace the one admin; the admin becomes member 1. IDs are never
-- reused (AUTOINCREMENT): actors in history name members as member:<id>.
CREATE TABLE members (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(username) BETWEEN 1 AND 64),
    password_hash       TEXT NOT NULL,   -- PHC string, argon2id
    language            TEXT NOT NULL CHECK (language IN ('en', 'ru')),
    webauthn_user_id    BLOB NOT NULL UNIQUE,   -- 32 random bytes: a passkey says nothing about the member
    jobs_seen_at        TEXT,
    created_at          TEXT NOT NULL,
    created_by          TEXT NOT NULL,   -- an actor: cli for the first member
    password_changed_at TEXT NOT NULL
) STRICT;
INSERT INTO members (id, username, password_hash, language, webauthn_user_id, jobs_seen_at, created_at, created_by, password_changed_at)
    SELECT id, username, password_hash, language, COALESCE(webauthn_user_id, randomblob(32)), jobs_seen_at, created_at, 'cli', password_changed_at
    FROM admin;

-- The children of admin are copied to point at members before admin goes:
-- with foreign keys on, dropping admin first would cascade to the sessions.
CREATE TABLE sessions_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   BLOB NOT NULL UNIQUE,
    member_id    INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    created_ip   TEXT NOT NULL,
    last_ip      TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    ended_at     TEXT,
    end_reason   TEXT CHECK (end_reason IN ('signed_out', 'signed_out_everywhere', 'password_changed', 'password_reset', 'second_factor_reset')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
INSERT INTO sessions_new SELECT * FROM sessions;
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
CREATE INDEX sessions_open ON sessions (member_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);

-- One authenticator app per member. The secret keeps its sealing: member 1's
-- AAD stays "admin:totp" (a migration has no master key to re-seal it), every
-- other member's is "member:<id>:totp".
CREATE TABLE totp_new (
    member_id  INTEGER PRIMARY KEY REFERENCES members (id) ON DELETE CASCADE,
    secret     BLOB NOT NULL,
    created_at TEXT NOT NULL,
    last_step  INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO totp_new (member_id, secret, created_at, last_step) SELECT 1, secret, created_at, last_step FROM totp;
DROP TABLE totp;
ALTER TABLE totp_new RENAME TO totp;

-- A credential ID stays unique across members: the browser names only it.
CREATE TABLE passkeys_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id     INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    credential_id BLOB NOT NULL UNIQUE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    credential    TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    last_used_at  TEXT
) STRICT;
INSERT INTO passkeys_new (id, member_id, credential_id, name, credential, created_at, last_used_at)
    SELECT id, 1, credential_id, name, credential, created_at, last_used_at FROM passkeys;
DROP TABLE passkeys;
ALTER TABLE passkeys_new RENAME TO passkeys;
CREATE INDEX passkeys_member ON passkeys (member_id);

DROP TABLE admin;

-- The member a sign-in attempt named; NULL when it named none (an unknown
-- username, a passkey of nobody). Successes so far were member 1's, so "new IP" stays quiet.
ALTER TABLE sign_in_attempts ADD COLUMN member_id INTEGER;
UPDATE sign_in_attempts SET member_id = 1 WHERE success = 1;

-- +goose Down
-- Only member 1 survives: the one admin.
CREATE TABLE admin (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    username            TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64),
    password_hash       TEXT NOT NULL,
    language            TEXT NOT NULL CHECK (language IN ('en', 'ru')),
    created_at          TEXT NOT NULL,
    password_changed_at TEXT NOT NULL,
    jobs_seen_at        TEXT,
    webauthn_user_id    BLOB
) STRICT;
INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at, jobs_seen_at, webauthn_user_id)
    SELECT id, username, password_hash, language, created_at, password_changed_at, jobs_seen_at, webauthn_user_id
    FROM members WHERE id = 1;

CREATE TABLE sessions_old (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   BLOB NOT NULL UNIQUE,
    admin_id     INTEGER NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    created_ip   TEXT NOT NULL,
    last_ip      TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    ended_at     TEXT,
    end_reason   TEXT CHECK (end_reason IN ('signed_out', 'signed_out_everywhere', 'password_changed', 'password_reset', 'second_factor_reset')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
INSERT INTO sessions_old SELECT * FROM sessions WHERE member_id = 1;
DROP TABLE sessions;
ALTER TABLE sessions_old RENAME TO sessions;
CREATE INDEX sessions_open ON sessions (admin_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE totp_old (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    secret     BLOB NOT NULL,
    created_at TEXT NOT NULL,
    last_step  INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO totp_old (id, secret, created_at, last_step) SELECT 1, secret, created_at, last_step FROM totp WHERE member_id = 1;
DROP TABLE totp;
ALTER TABLE totp_old RENAME TO totp;

CREATE TABLE passkeys_old (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id BLOB NOT NULL UNIQUE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    credential    TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    last_used_at  TEXT
) STRICT;
INSERT INTO passkeys_old (id, credential_id, name, credential, created_at, last_used_at)
    SELECT id, credential_id, name, credential, created_at, last_used_at FROM passkeys WHERE member_id = 1;
DROP TABLE passkeys;
ALTER TABLE passkeys_old RENAME TO passkeys;

DROP TABLE members;

ALTER TABLE sign_in_attempts DROP COLUMN member_id;
