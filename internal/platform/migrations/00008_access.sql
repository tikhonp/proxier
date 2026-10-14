-- +goose Up

-- members gains its setup and its removal. A member added in Settings has no
-- password until they finish setup with their link; a removed member keeps
-- the row, so history still names them, and frees the username: adding them
-- again takes it back. SQLite can't drop a column's UNIQUE or NOT NULL in
-- place, so members is copied, and with it the tables that reference it:
-- with foreign keys on, dropping members while they still pointed at it
-- would cascade to them. The copies point at members_new, which the rename
-- turns into members.
CREATE TABLE members_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            TEXT NOT NULL COLLATE NOCASE CHECK (length(username) BETWEEN 1 AND 64),
    password_hash       TEXT,            -- PHC string, argon2id; NULL until the member finishes setup
    language            TEXT NOT NULL CHECK (language IN ('en', 'ru')),
    webauthn_user_id    BLOB NOT NULL UNIQUE,
    jobs_seen_at        TEXT,
    created_at          TEXT NOT NULL,
    created_by          TEXT NOT NULL,   -- an actor: cli for the first member
    password_changed_at TEXT,
    removed_at          TEXT,
    removed_by          TEXT,            -- an actor
    CHECK ((password_hash IS NULL) = (password_changed_at IS NULL)),
    CHECK ((removed_at IS NULL) = (removed_by IS NULL))
) STRICT;
INSERT INTO members_new (id, username, password_hash, language, webauthn_user_id, jobs_seen_at, created_at, created_by, password_changed_at)
    SELECT id, username, password_hash, language, webauthn_user_id, jobs_seen_at, created_at, created_by, password_changed_at FROM members;
-- IDs are never reused: the copy's counter starts where the old one was.
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'members'), 0)) WHERE name = 'members_new';

-- sessions also gains the end reason member_removed.
CREATE TABLE sessions_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   BLOB NOT NULL UNIQUE,
    member_id    INTEGER NOT NULL REFERENCES members_new (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    created_ip   TEXT NOT NULL,
    last_ip      TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    ended_at     TEXT,
    end_reason   TEXT CHECK (end_reason IN ('signed_out', 'signed_out_everywhere', 'password_changed', 'password_reset', 'second_factor_reset', 'member_removed')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
INSERT INTO sessions_new SELECT * FROM sessions;
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'sessions'), 0)) WHERE name = 'sessions_new';

CREATE TABLE totp_new (
    member_id  INTEGER PRIMARY KEY REFERENCES members_new (id) ON DELETE CASCADE,
    secret     BLOB NOT NULL,
    created_at TEXT NOT NULL,
    last_step  INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO totp_new SELECT member_id, secret, created_at, last_step FROM totp;

CREATE TABLE passkeys_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id     INTEGER NOT NULL REFERENCES members_new (id) ON DELETE CASCADE,
    credential_id BLOB NOT NULL UNIQUE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    credential    TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    last_used_at  TEXT
) STRICT;
INSERT INTO passkeys_new SELECT id, member_id, credential_id, name, credential, created_at, last_used_at FROM passkeys;
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'passkeys'), 0)) WHERE name = 'passkeys_new';

DROP TABLE sessions;
DROP TABLE totp;
DROP TABLE passkeys;
DROP TABLE members;
ALTER TABLE members_new RENAME TO members;
ALTER TABLE sessions_new RENAME TO sessions;
ALTER TABLE totp_new RENAME TO totp;
ALTER TABLE passkeys_new RENAME TO passkeys;
CREATE UNIQUE INDEX members_username ON members (username) WHERE removed_at IS NULL;
CREATE INDEX sessions_open ON sessions (member_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);
CREATE INDEX passkeys_member ON passkeys (member_id);

-- A member's level in each area; no row is none. Each area has its own
-- ladder of levels (internal/platform/access).
CREATE TABLE member_access (
    member_id INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    area      TEXT NOT NULL,
    level     TEXT NOT NULL,
    PRIMARY KEY (member_id, area),
    CHECK ((area = 'servers' AND level IN ('view', 'manage'))
        OR (area IN ('subscriptions', 'routing', 'routerscripts') AND level IN ('own', 'view_all', 'manage_all'))
        OR (area = 'activity' AND level IN ('own', 'all'))
        OR (area = 'settings' AND level = 'manage'))
) STRICT, WITHOUT ROWID;
-- Every member so far was the one admin: Owner, the top level everywhere.
INSERT INTO member_access (member_id, area, level)
    SELECT m.id, a.area, a.level FROM members m,
    (SELECT 'servers' AS area, 'manage' AS level UNION ALL SELECT 'subscriptions', 'manage_all'
     UNION ALL SELECT 'routing', 'manage_all' UNION ALL SELECT 'routerscripts', 'manage_all'
     UNION ALL SELECT 'activity', 'all' UNION ALL SELECT 'settings', 'manage') a;

-- The secret URL a new member chooses their password at. Single use, 24
-- hours, found by the HMAC of its token; the token itself is sealed (AAD
-- "setup_link:<id>:token") only so the Members page can copy it again, and
-- erased when the link ends.
CREATE TABLE member_setup_links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id    INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    token_lookup BLOB NOT NULL UNIQUE,
    token        BLOB,
    created_at   TEXT NOT NULL,
    created_by   TEXT NOT NULL,     -- an actor
    expires_at   TEXT NOT NULL,
    ended_at     TEXT,
    end_reason   TEXT CHECK (end_reason IN ('used', 'replaced', 'removed')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL)),
    CHECK (ended_at IS NULL OR token IS NULL)
) STRICT;
CREATE UNIQUE INDEX member_setup_links_open ON member_setup_links (member_id) WHERE ended_at IS NULL;

-- The first member is added like any other now, and a member's language is
-- theirs: the two admin.* event types take the member.* names.
UPDATE events SET type = 'member.added' WHERE type = 'admin.created';
UPDATE events SET type = 'member.language_changed' WHERE type = 'admin.language_changed';
UPDATE notification_rules SET event_type = 'member.language_changed' WHERE event_type = 'admin.language_changed';
UPDATE notification_rules SET event_type = 'member.added' WHERE event_type = 'admin.created';

-- +goose Down
-- Going back to one way of being a member: every member who was removed or
-- hasn't finished setup goes, with their rows.
UPDATE events SET type = 'admin.created' WHERE type = 'member.added' AND actor = 'cli';
UPDATE events SET type = 'admin.language_changed' WHERE type = 'member.language_changed';
UPDATE notification_rules SET event_type = 'admin.language_changed' WHERE event_type = 'member.language_changed';
DELETE FROM notification_rules WHERE event_type = 'member.added';

DROP TABLE member_setup_links;
DROP TABLE member_access;

CREATE TABLE members_old (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(username) BETWEEN 1 AND 64),
    password_hash       TEXT NOT NULL,
    language            TEXT NOT NULL CHECK (language IN ('en', 'ru')),
    webauthn_user_id    BLOB NOT NULL UNIQUE,
    jobs_seen_at        TEXT,
    created_at          TEXT NOT NULL,
    created_by          TEXT NOT NULL,
    password_changed_at TEXT NOT NULL
) STRICT;
INSERT INTO members_old (id, username, password_hash, language, webauthn_user_id, jobs_seen_at, created_at, created_by, password_changed_at)
    SELECT id, username, password_hash, language, webauthn_user_id, jobs_seen_at, created_at, created_by, password_changed_at
    FROM members WHERE removed_at IS NULL AND password_hash IS NOT NULL;
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'members'), 0)) WHERE name = 'members_old';

CREATE TABLE sessions_old (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   BLOB NOT NULL UNIQUE,
    member_id    INTEGER NOT NULL REFERENCES members_old (id) ON DELETE CASCADE,
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
INSERT INTO sessions_old SELECT * FROM sessions WHERE member_id IN (SELECT id FROM members_old) AND end_reason IS NOT 'member_removed';
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'sessions'), 0)) WHERE name = 'sessions_old';

CREATE TABLE totp_old (
    member_id  INTEGER PRIMARY KEY REFERENCES members_old (id) ON DELETE CASCADE,
    secret     BLOB NOT NULL,
    created_at TEXT NOT NULL,
    last_step  INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO totp_old SELECT member_id, secret, created_at, last_step FROM totp WHERE member_id IN (SELECT id FROM members_old);

CREATE TABLE passkeys_old (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id     INTEGER NOT NULL REFERENCES members_old (id) ON DELETE CASCADE,
    credential_id BLOB NOT NULL UNIQUE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    credential    TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    last_used_at  TEXT
) STRICT;
INSERT INTO passkeys_old SELECT id, member_id, credential_id, name, credential, created_at, last_used_at FROM passkeys WHERE member_id IN (SELECT id FROM members_old);
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'passkeys'), 0)) WHERE name = 'passkeys_old';

DROP TABLE sessions;
DROP TABLE totp;
DROP TABLE passkeys;
DROP TABLE members;
ALTER TABLE members_old RENAME TO members;
ALTER TABLE sessions_old RENAME TO sessions;
ALTER TABLE totp_old RENAME TO totp;
ALTER TABLE passkeys_old RENAME TO passkeys;
CREATE INDEX sessions_open ON sessions (member_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_ended ON sessions (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX sessions_expires ON sessions (expires_at);
CREATE INDEX passkeys_member ON passkeys (member_id);
