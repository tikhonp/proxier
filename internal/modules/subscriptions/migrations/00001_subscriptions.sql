-- +goose Up

CREATE TABLE subs_subscriptions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT NOT NULL UNIQUE,
    title            TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    formats          TEXT NOT NULL DEFAULT 'uri-plain,uri-base64', -- allowed, comma-joined, in output.Names() order
    default_format   TEXT NOT NULL DEFAULT 'uri-plain',
    update_hours     INTEGER NOT NULL DEFAULT 12 CHECK (update_hours BETWEEN 1 AND 168),
    hide_unhealthy   INTEGER NOT NULL DEFAULT 0 CHECK (hide_unhealthy IN (0,1)),
    hide_states      TEXT NOT NULL DEFAULT 'blocked,down',          -- of blocked, down, degraded, unknown
    hide_grace_min   INTEGER NOT NULL DEFAULT 30 CHECK (hide_grace_min BETWEEN 0 AND 10080),
    auto_add         INTEGER NOT NULL DEFAULT 0 CHECK (auto_add IN (0,1)),
    auto_add_since   TEXT,                                          -- when auto_add was last turned on
    all_unhealthy_at TEXT,                                          -- the last subscription.all_unhealthy (at most hourly, 2b)
    created_at       TEXT NOT NULL,
    CHECK ((auto_add = 1) = (auto_add_since IS NOT NULL))
) STRICT;

-- The servers of a subscription, in order. server_id is the servers module's
-- id; the name is a copy (server names never change and are never reused), so
-- a page can name a member the catalog no longer serves.
CREATE TABLE subs_subscription_servers (
    subscription_id INTEGER NOT NULL REFERENCES subs_subscriptions(id) ON DELETE CASCADE,
    server_id       INTEGER NOT NULL,
    server_name     TEXT NOT NULL,
    position        INTEGER NOT NULL,                               -- 1..n, dense, rewritten on every change
    added_at        TEXT NOT NULL,
    PRIMARY KEY (subscription_id, server_id)
) STRICT;
CREATE INDEX subs_subscription_servers_server ON subs_subscription_servers(server_id);

CREATE TABLE subs_links (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    -- NULL only for a deleted link whose subscription was deleted after it
    subscription_id    INTEGER REFERENCES subs_subscriptions(id) ON DELETE SET NULL,
    name               TEXT NOT NULL,
    note               TEXT NOT NULL DEFAULT '',
    token              BLOB,                                        -- vault "link:<id>:token"; NULL once the tombstone ended
    token_lookup       BLOB UNIQUE,                                 -- vault.Lookup(token); NULL with the token
    state              TEXT NOT NULL CHECK (state IN ('active','disabled','deleted')),
    expires_at         TEXT,                                        -- the first moment the link is expired; NULL = never
    language           TEXT NOT NULL CHECK (language IN ('en','ru')),
    format             TEXT,                                        -- override; NULL = the subscription's default
    alert_networks     INTEGER CHECK (alert_networks BETWEEN 1 AND 1000), -- override; NULL = the setting
    alert_apps         INTEGER CHECK (alert_apps BETWEEN 1 AND 1000),
    alerts_muted       INTEGER NOT NULL DEFAULT 0 CHECK (alerts_muted IN (0,1)),
    alerted_at         TEXT,                                        -- the last link.shared_suspected
    expiry_warned_at   TEXT,                                        -- link.expiring_soon sent for the current expiry
    expired_at         TEXT,                                        -- link.expired sent for the current expiry
    created_at         TEXT NOT NULL,
    disabled_at        TEXT,
    deleted_at         TEXT,
    last_fetch_at      TEXT,
    last_fetch_app     TEXT NOT NULL DEFAULT '',
    last_fetch_network TEXT NOT NULL DEFAULT '',
    CHECK (state = 'deleted' OR subscription_id IS NOT NULL),
    CHECK ((state = 'deleted') = (deleted_at IS NOT NULL)),
    CHECK ((token IS NULL) = (token_lookup IS NULL))
) STRICT;
-- Names are unique among links that aren't deleted.
CREATE UNIQUE INDEX subs_links_live_name ON subs_links(name) WHERE state <> 'deleted';
CREATE INDEX subs_links_subscription ON subs_links(subscription_id);

CREATE TABLE subs_fetches (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES subs_links(id),
    at         TEXT NOT NULL,
    ip         TEXT NOT NULL,
    network    TEXT NOT NULL,                                       -- IPv4 /24 or IPv6 /48: "198.51.100.0/24"
    user_agent TEXT NOT NULL DEFAULT '',                            -- at most 256 characters
    app        TEXT NOT NULL DEFAULT '',                            -- the app family; '' = unknown (counted by user_agent)
    format     TEXT NOT NULL,
    outcome    TEXT NOT NULL CHECK (outcome IN ('ok','stub-disabled','stub-expired','stub-deleted','stub-empty'))
) STRICT;
CREATE INDEX subs_fetches_link ON subs_fetches(link_id, at);
CREATE INDEX subs_fetches_at ON subs_fetches(at);

-- The country of each network seen in fetches, looked up by its first address
-- (2c). '' = unknown: the lookup is off or failed.
CREATE TABLE subs_network_countries (
    network      TEXT PRIMARY KEY,
    country      TEXT NOT NULL DEFAULT '',
    looked_up_at TEXT NOT NULL
) STRICT;

-- A cut-off: the link was disabled and its subscription's servers are rotated
-- one at a time (2d).
CREATE TABLE subs_cutoffs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES subs_links(id),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
) STRICT;
CREATE INDEX subs_cutoffs_link ON subs_cutoffs(link_id, id);

CREATE TABLE subs_cutoff_items (
    cutoff_id   INTEGER NOT NULL REFERENCES subs_cutoffs(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    server_id   INTEGER NOT NULL,
    server_name TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('waiting','running','done','failed','skipped')),
    job_id      INTEGER,                                            -- the rotation job, by value
    error       TEXT NOT NULL DEFAULT '',                           -- failed: the rotation's error; skipped: why
    PRIMARY KEY (cutoff_id, position)
) STRICT;
CREATE INDEX subs_cutoff_items_job ON subs_cutoff_items(job_id);
-- One rotation at a time per cut-off.
CREATE UNIQUE INDEX subs_cutoff_items_running ON subs_cutoff_items(cutoff_id) WHERE state = 'running';

-- +goose Down
DROP TABLE subs_cutoff_items;
DROP TABLE subs_cutoffs;
DROP TABLE subs_network_countries;
DROP TABLE subs_fetches;
DROP TABLE subs_links;
DROP TABLE subs_subscription_servers;
DROP TABLE subs_subscriptions;
