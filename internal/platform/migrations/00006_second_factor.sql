-- +goose Up

-- The authenticator app. One row while it is on; none while it is off.
CREATE TABLE totp (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    secret     BLOB NOT NULL,                 -- vault, AAD "admin:totp"; 20 random bytes
    created_at TEXT NOT NULL,
    last_step  INTEGER NOT NULL DEFAULT 0     -- the newest 30 s step whose code was used: no code works twice
) STRICT;

-- Passkeys. A passkey signs in alone; the public key isn't a secret.
CREATE TABLE passkeys (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id BLOB NOT NULL UNIQUE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    credential    TEXT NOT NULL,              -- go-webauthn's Credential as JSON: public key, sign count, flags
    created_at    TEXT NOT NULL,
    last_used_at  TEXT
) STRICT;

-- The WebAuthn user handle: random, so a passkey says nothing about the admin.
ALTER TABLE admin ADD COLUMN webauthn_user_id BLOB;
UPDATE admin SET webauthn_user_id = randomblob(32);

-- sessions gains the end reason second_factor_reset; SQLite can't change a
-- CHECK in place, so the table is copied.
CREATE TABLE sessions_new (
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
INSERT INTO sessions_new SELECT * FROM sessions;
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
CREATE INDEX sessions_open ON sessions (admin_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);

-- +goose Down
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
    end_reason   TEXT CHECK (end_reason IN ('signed_out', 'signed_out_everywhere', 'password_changed', 'password_reset')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
INSERT INTO sessions_old
    SELECT id, token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip, user_agent, ended_at,
           CASE end_reason WHEN 'second_factor_reset' THEN 'password_reset' ELSE end_reason END
    FROM sessions;
DROP TABLE sessions;
ALTER TABLE sessions_old RENAME TO sessions;
CREATE INDEX sessions_open ON sessions (admin_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);

ALTER TABLE admin DROP COLUMN webauthn_user_id;
DROP TABLE passkeys;
DROP TABLE totp;
