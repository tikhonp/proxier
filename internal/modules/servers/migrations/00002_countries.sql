-- Servers have a country instead of a location (ADR 0015): the location's
-- code, name and flag are now derived from the server's country, and the
-- locations table goes.
--
-- servers_servers is rebuilt to lose location_id, whose REFERENCES clause
-- SQLite can't drop in place. That needs foreign keys off: with them on,
-- dropping the old table deletes its rows first, and ON DELETE CASCADE would
-- take every row that references a server with them. SQLite ignores that
-- pragma inside a transaction, so this migration runs outside goose's and
-- opens its own; a failure leaves it uncommitted, and the start stops.
--
-- The locations are kept as JSON in servers_meta ('locations.archive') so
-- that Down gives back the names, ids and dates the admin entered. Nothing
-- reads it otherwise.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

INSERT OR REPLACE INTO servers_meta (key, value)
SELECT 'locations.archive', json_group_array(json_object(
    'id', id, 'code', code, 'name', name, 'country', country, 'last_number', last_number, 'created_at', created_at))
FROM (SELECT * FROM servers_locations ORDER BY id);

CREATE TABLE servers_servers_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    -- ISO 3166-1 alpha-2, upper case; its lower case is the first half of the
    -- name of a server created since (a server keeps the name it was given)
    country             TEXT NOT NULL CHECK (length(country) = 2 AND country GLOB '[A-Z][A-Z]'),
    number              INTEGER NOT NULL,
    name                TEXT NOT NULL UNIQUE,
    ip                  TEXT NOT NULL,
    ssh_port            INTEGER NOT NULL DEFAULT 22,
    management_hostname TEXT NOT NULL,
    proxy_hostname      TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN ('provisioning','active','failed','retired')),
    template_id         INTEGER NOT NULL REFERENCES servers_templates(id),
    template_version    INTEGER NOT NULL,
    params              TEXT NOT NULL DEFAULT '{}',
    params_secret       BLOB,
    notes               TEXT NOT NULL DEFAULT '',
    provision_job_id    INTEGER,
    retire_job_id       INTEGER,
    failed_step         TEXT,
    failed_error        TEXT,
    health              TEXT CHECK (health IN ('healthy','degraded','blocked','down','unknown','paused')),
    health_since        TEXT,
    health_reason       TEXT NOT NULL DEFAULT '',
    health_detail       TEXT NOT NULL DEFAULT '{}',
    candidate           TEXT,
    candidate_count     INTEGER NOT NULL DEFAULT 0,
    counted_proxy_at    TEXT,
    checks_paused_until TEXT,
    reminded_at         TEXT,
    cert_warned         INTEGER NOT NULL DEFAULT 0 CHECK (cert_warned IN (0,1)),
    disk_warned         INTEGER NOT NULL DEFAULT 0 CHECK (disk_warned IN (0,1)),
    created_at          TEXT NOT NULL,
    activated_at        TEXT,
    retired_at          TEXT,
    CHECK ((state = 'active') = (health IS NOT NULL))
) STRICT;

-- Every server takes its location's country. A LEFT JOIN, so a server without
-- one fails the NOT NULL above instead of being left behind. The location
-- picker offered withdrawn codes beside the countries that replaced them
-- under the same name (UK and GB were both "United Kingdom"); such a code
-- becomes its successor. Those without one (SU, YU, AN…) had no name in the
-- picker and stay as they are, shown as the bare code.
INSERT INTO servers_servers_new (id, country, number, name, ip, ssh_port, management_hostname, proxy_hostname, state,
    template_id, template_version, params, params_secret, notes, provision_job_id, retire_job_id, failed_step, failed_error,
    health, health_since, health_reason, health_detail, candidate, candidate_count, counted_proxy_at, checks_paused_until,
    reminded_at, cert_warned, disk_warned, created_at, activated_at, retired_at)
SELECT s.id, CASE upper(l.country)
        WHEN 'UK' THEN 'GB' WHEN 'DD' THEN 'DE' WHEN 'FX' THEN 'FR' WHEN 'BU' THEN 'MM' WHEN 'CT' THEN 'KI'
        WHEN 'DY' THEN 'BJ' WHEN 'HV' THEN 'BF' WHEN 'NH' THEN 'VU' WHEN 'NQ' THEN 'AQ' WHEN 'PZ' THEN 'PA'
        WHEN 'RH' THEN 'ZW' WHEN 'TP' THEN 'TL' WHEN 'VD' THEN 'VN' WHEN 'YD' THEN 'YE' WHEN 'ZR' THEN 'CD'
        WHEN 'JT' THEN 'UM' WHEN 'MI' THEN 'UM' WHEN 'PU' THEN 'UM' WHEN 'WK' THEN 'UM'
        ELSE upper(l.country) END,
    s.number, s.name, s.ip, s.ssh_port, s.management_hostname, s.proxy_hostname, s.state,
    s.template_id, s.template_version, s.params, s.params_secret, s.notes, s.provision_job_id, s.retire_job_id, s.failed_step, s.failed_error,
    s.health, s.health_since, s.health_reason, s.health_detail, s.candidate, s.candidate_count, s.counted_proxy_at, s.checks_paused_until,
    s.reminded_at, s.cert_warned, s.disk_warned, s.created_at, s.activated_at, s.retired_at
FROM servers_servers s LEFT JOIN servers_locations l ON l.id = s.location_id;

-- Ids are never reused (other modules keep them): carry the sequence over.
UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'servers_servers'), 0))
WHERE name = 'servers_servers_new';
INSERT INTO sqlite_sequence (name, seq)
SELECT 'servers_servers_new', seq FROM sqlite_sequence
WHERE name = 'servers_servers' AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'servers_servers_new');

DROP TABLE servers_servers;
ALTER TABLE servers_servers_new RENAME TO servers_servers;
CREATE UNIQUE INDEX servers_servers_live_ip ON servers_servers(ip) WHERE state <> 'retired';
DROP TABLE servers_locations;

-- A broken reference fails the migration (the CHECK) rather than committing.
CREATE TEMP TABLE servers_fk_check (problems INTEGER NOT NULL CHECK (problems = 0));
INSERT INTO servers_fk_check SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE servers_fk_check;

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE servers_locations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    code        TEXT NOT NULL UNIQUE CHECK (length(code) BETWEEN 2 AND 5),
    name        TEXT NOT NULL,
    country     TEXT NOT NULL CHECK (length(country) = 2),
    last_number INTEGER NOT NULL DEFAULT 0,  -- the highest number ever used; never decreases
    created_at  TEXT NOT NULL
) STRICT;

-- The locations as they were before Up.
INSERT INTO servers_locations (id, code, name, country, last_number, created_at)
SELECT json_extract(j.value, '$.id'), json_extract(j.value, '$.code'), json_extract(j.value, '$.name'),
    json_extract(j.value, '$.country'), json_extract(j.value, '$.last_number'), json_extract(j.value, '$.created_at')
FROM servers_meta m, json_each(m.value) j
WHERE m.key = 'locations.archive';

-- A name prefix first used since then becomes a location of its servers'
-- country, named by the country code (names were not kept).
INSERT INTO servers_locations (code, name, country, created_at)
SELECT p.prefix, min(p.country), min(p.country), min(p.created_at)
FROM (SELECT substr(name, 1, length(name) - length(CAST(number AS TEXT)) - 1) AS prefix, country, created_at FROM servers_servers) p
WHERE p.prefix NOT IN (SELECT code FROM servers_locations)
GROUP BY p.prefix;

UPDATE servers_locations SET last_number = max(last_number, COALESCE((SELECT max(s.number) FROM servers_servers s
    WHERE substr(s.name, 1, length(s.name) - length(CAST(s.number AS TEXT)) - 1) = servers_locations.code), 0));

CREATE TABLE servers_servers_old (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id         INTEGER NOT NULL REFERENCES servers_locations(id),
    number              INTEGER NOT NULL,
    name                TEXT NOT NULL UNIQUE,
    ip                  TEXT NOT NULL,
    ssh_port            INTEGER NOT NULL DEFAULT 22,
    management_hostname TEXT NOT NULL,
    proxy_hostname      TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN ('provisioning','active','failed','retired')),
    template_id         INTEGER NOT NULL REFERENCES servers_templates(id),
    template_version    INTEGER NOT NULL,    -- the version in force (that of the current files: 1d, 1e)
    params              TEXT NOT NULL DEFAULT '{}', -- non-secret parameters, JSON
    params_secret       BLOB,                -- vault "server:<id>:params": JSON of secret parameters
    notes               TEXT NOT NULL DEFAULT '',
    -- provisioning
    provision_job_id    INTEGER,
    retire_job_id       INTEGER,             -- set when retirement starts ("retiring")
    failed_step         TEXT,
    failed_error        TEXT,
    -- health (active only)
    health              TEXT CHECK (health IN ('healthy','degraded','blocked','down','unknown','paused')),
    health_since        TEXT,
    health_reason       TEXT NOT NULL DEFAULT '',
    health_detail       TEXT NOT NULL DEFAULT '{}', -- JSON: the latest evaluation's per-check summary
    candidate           TEXT,                -- the last counted evaluation's candidate
    candidate_count     INTEGER NOT NULL DEFAULT 0,
    counted_proxy_at    TEXT,                -- the proxy-test round the last counted evaluation used (1f flap rule)
    checks_paused_until TEXT,                -- '9999-12-31T00:00:00.000Z' = until resumed
    reminded_at         TEXT,                -- last server.still_unhealthy
    cert_warned         INTEGER NOT NULL DEFAULT 0 CHECK (cert_warned IN (0,1)), -- cert_expiring sent for this crossing
    disk_warned         INTEGER NOT NULL DEFAULT 0 CHECK (disk_warned IN (0,1)), -- disk_low sent for this crossing
    created_at          TEXT NOT NULL,
    activated_at        TEXT,
    retired_at          TEXT,
    UNIQUE (location_id, number),
    CHECK ((state = 'active') = (health IS NOT NULL))
) STRICT;

INSERT INTO servers_servers_old (id, location_id, number, name, ip, ssh_port, management_hostname, proxy_hostname, state,
    template_id, template_version, params, params_secret, notes, provision_job_id, retire_job_id, failed_step, failed_error,
    health, health_since, health_reason, health_detail, candidate, candidate_count, counted_proxy_at, checks_paused_until,
    reminded_at, cert_warned, disk_warned, created_at, activated_at, retired_at)
SELECT s.id, (SELECT l.id FROM servers_locations l WHERE l.code = substr(s.name, 1, length(s.name) - length(CAST(s.number AS TEXT)) - 1)),
    s.number, s.name, s.ip, s.ssh_port, s.management_hostname, s.proxy_hostname, s.state,
    s.template_id, s.template_version, s.params, s.params_secret, s.notes, s.provision_job_id, s.retire_job_id, s.failed_step, s.failed_error,
    s.health, s.health_since, s.health_reason, s.health_detail, s.candidate, s.candidate_count, s.counted_proxy_at, s.checks_paused_until,
    s.reminded_at, s.cert_warned, s.disk_warned, s.created_at, s.activated_at, s.retired_at
FROM servers_servers s;

UPDATE sqlite_sequence SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'servers_servers'), 0))
WHERE name = 'servers_servers_old';
INSERT INTO sqlite_sequence (name, seq)
SELECT 'servers_servers_old', seq FROM sqlite_sequence
WHERE name = 'servers_servers' AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'servers_servers_old');

DROP TABLE servers_servers;
ALTER TABLE servers_servers_old RENAME TO servers_servers;
CREATE UNIQUE INDEX servers_servers_live_ip ON servers_servers(ip) WHERE state <> 'retired';
DELETE FROM servers_meta WHERE key = 'locations.archive';

CREATE TEMP TABLE servers_fk_check (problems INTEGER NOT NULL CHECK (problems = 0));
INSERT INTO servers_fk_check SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE servers_fk_check;

COMMIT;
PRAGMA foreign_keys = ON;
