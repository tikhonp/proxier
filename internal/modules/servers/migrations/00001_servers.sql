-- +goose Up

-- Run-once markers of the module (the seed).
CREATE TABLE servers_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE servers_locations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    code        TEXT NOT NULL UNIQUE CHECK (length(code) BETWEEN 2 AND 5),
    name        TEXT NOT NULL,
    country     TEXT NOT NULL CHECK (length(country) = 2),
    last_number INTEGER NOT NULL DEFAULT 0,  -- the highest number ever used; never decreases
    created_at  TEXT NOT NULL
) STRICT;

CREATE TABLE servers_templates (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    slug            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    default_version INTEGER,                 -- a version number; NULL before the first publish
    archived_at     TEXT,
    created_at      TEXT NOT NULL
) STRICT;

-- At most one draft per template.
CREATE TABLE servers_template_drafts (
    template_id INTEGER PRIMARY KEY REFERENCES servers_templates(id) ON DELETE CASCADE,
    based_on    INTEGER,                     -- version number; NULL for a new template
    revision    INTEGER NOT NULL DEFAULT 1,
    source      TEXT NOT NULL DEFAULT '{}',  -- JSON: {"kind":"zip","name"} / {"kind":"git","url","ref","commit","path"} / {"kind":"version","version"}
    updated_at  TEXT NOT NULL,
    updated_by  TEXT NOT NULL                -- actor
) STRICT;

CREATE TABLE servers_draft_files (
    template_id INTEGER NOT NULL REFERENCES servers_template_drafts(template_id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    content     BLOB NOT NULL,
    PRIMARY KEY (template_id, path)
) STRICT;

CREATE TABLE servers_template_versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id  INTEGER NOT NULL REFERENCES servers_templates(id) ON DELETE CASCADE,
    number       INTEGER NOT NULL CHECK (number >= 1),
    notes        TEXT NOT NULL DEFAULT '',
    warnings     TEXT NOT NULL DEFAULT '[]', -- JSON findings at publish
    source       TEXT NOT NULL DEFAULT '{}', -- the draft's source, kept
    published_at TEXT NOT NULL,
    published_by TEXT NOT NULL,
    UNIQUE (template_id, number)
) STRICT;

CREATE TABLE servers_template_files (
    version_id INTEGER NOT NULL REFERENCES servers_template_versions(id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    content    BLOB NOT NULL,
    PRIMARY KEY (version_id, path)
) STRICT;

CREATE TABLE servers_servers (
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

-- An IP belongs to at most one non-retired server.
CREATE UNIQUE INDEX servers_servers_live_ip ON servers_servers(ip) WHERE state <> 'retired';

CREATE TABLE servers_generated_values (
    server_id  INTEGER NOT NULL REFERENCES servers_servers(id),
    key        TEXT NOT NULL,
    value      BLOB NOT NULL,                -- vault "server:<id>:gen:<key>"
    pending    BLOB,                         -- a rotation's new value, same AAD, until committed
    created_at TEXT NOT NULL,
    rotated_at TEXT,
    PRIMARY KEY (server_id, key)
) STRICT;

CREATE TABLE servers_endpoints (
    server_id    INTEGER NOT NULL REFERENCES servers_servers(id),
    key          TEXT NOT NULL,
    type         TEXT NOT NULL,
    host         TEXT NOT NULL,
    port         INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    secret       BLOB NOT NULL,              -- vault "server:<id>:endpoint:<key>": {"credential","params"}
    display_name TEXT NOT NULL,
    position     INTEGER NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (server_id, key)
) STRICT;

CREATE TABLE servers_deployments (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id        INTEGER NOT NULL REFERENCES servers_servers(id),
    kind             TEXT NOT NULL CHECK (kind IN ('provision','redeploy','upgrade','params','rotate','restore','restart','images','reboot')),
    template_version INTEGER NOT NULL,
    params           TEXT NOT NULL DEFAULT '{}',
    params_secret    BLOB,                   -- vault "deployment:<id>:params"
    job_id           INTEGER,
    state            TEXT NOT NULL CHECK (state IN ('running','succeeded','failed')),
    uploaded         INTEGER NOT NULL DEFAULT 0 CHECK (uploaded IN (0,1)), -- 1: it has servers_deployed_files (the "current files" rule)
    files_changed    INTEGER NOT NULL DEFAULT 0,
    started_at       TEXT NOT NULL,
    finished_at      TEXT,
    error            TEXT
) STRICT;
CREATE INDEX servers_deployments_server ON servers_deployments(server_id, id);
CREATE INDEX servers_deployments_current ON servers_deployments(server_id, id) WHERE state = 'succeeded' AND uploaded = 1;

-- Files a deployment rendered. Only deployments that upload files have rows.
CREATE TABLE servers_deployed_files (
    deployment_id INTEGER NOT NULL REFERENCES servers_deployments(id) ON DELETE CASCADE,
    path          TEXT NOT NULL,
    mode          INTEGER NOT NULL,
    sha256        TEXT NOT NULL,
    content       BLOB NOT NULL,             -- vault "deployment:<id>:file:<path>"
    PRIMARY KEY (deployment_id, path)
) STRICT;

CREATE TABLE servers_dns_records (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id  INTEGER NOT NULL REFERENCES servers_servers(id),
    provider   TEXT NOT NULL,                -- 'cloudflare'
    zone_id    TEXT NOT NULL,
    zone       TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL CHECK (type IN ('A')),
    content    TEXT NOT NULL,
    record_id  TEXT NOT NULL,
    created_at TEXT NOT NULL,
    deleted_at TEXT,
    kept       TEXT                          -- why retirement kept it ("points elsewhere")
) STRICT;

CREATE TABLE servers_check_results (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL REFERENCES servers_servers(id),
    kind         TEXT NOT NULL CHECK (kind IN ('self','proxy','external')),
    endpoint_key TEXT NOT NULL DEFAULT '',   -- proxy tests
    vantage      TEXT NOT NULL,              -- 'home' or a check-host node name
    at           TEXT NOT NULL,
    ok           INTEGER NOT NULL CHECK (ok IN (0,1)),
    class        TEXT NOT NULL DEFAULT '',   -- 'unreachable', 'stack-failed', 'host-key-changed', 'tcp-timeout', 'stalled', 'skipped', …
    inconclusive INTEGER NOT NULL DEFAULT 0 CHECK (inconclusive IN (0,1)),
    detail       TEXT NOT NULL DEFAULT '{}'  -- JSON: timings, per-check items, the URL used, node answers
) STRICT;
CREATE INDEX servers_check_results_latest ON servers_check_results(server_id, kind, endpoint_key, vantage, at);
CREATE INDEX servers_check_results_at ON servers_check_results(at);

-- Home's reference check. Not per server.
CREATE TABLE servers_reference_results (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    at     TEXT NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('online','foreign-unreachable','offline')),
    detail TEXT NOT NULL DEFAULT '{}'
) STRICT;
CREATE INDEX servers_reference_results_at ON servers_reference_results(at);

-- Home's current connectivity state. One row.
CREATE TABLE servers_home (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    state TEXT NOT NULL CHECK (state IN ('online','foreign-unreachable','offline')),
    since TEXT NOT NULL
) STRICT;

-- check-host.net nodes, refreshed daily.
CREATE TABLE servers_checkhost_nodes (
    name         TEXT PRIMARY KEY,           -- "ru1.node.check-host.net"
    country      TEXT NOT NULL,              -- ISO alpha-2
    city         TEXT NOT NULL DEFAULT '',
    refreshed_at TEXT NOT NULL
) STRICT;

CREATE TABLE servers_metric_samples (
    server_id  INTEGER NOT NULL REFERENCES servers_servers(id),
    at         TEXT NOT NULL,
    load1      REAL NOT NULL,
    cpu_pct    REAL,                         -- NULL on the first sample after activation or reboot
    mem_used   INTEGER NOT NULL,
    mem_total  INTEGER NOT NULL,
    disk_used  INTEGER NOT NULL,
    disk_total INTEGER NOT NULL,
    rx_bytes   INTEGER,                      -- delta since the previous sample; NULL when unknown
    tx_bytes   INTEGER,
    cpu_busy   INTEGER NOT NULL,             -- raw /proc/stat counters for the next delta
    cpu_total  INTEGER NOT NULL,
    rx_counter INTEGER NOT NULL,
    tx_counter INTEGER NOT NULL,
    uptime     REAL NOT NULL,
    containers TEXT NOT NULL DEFAULT '[]',   -- JSON: name, state, restarts
    PRIMARY KEY (server_id, at)
) STRICT;

CREATE TABLE servers_metric_hourly (
    server_id   INTEGER NOT NULL REFERENCES servers_servers(id),
    hour        TEXT NOT NULL,               -- start of the UTC hour
    samples     INTEGER NOT NULL,
    load1       REAL NOT NULL,
    cpu_pct     REAL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    rx_bytes    INTEGER NOT NULL,            -- sums
    tx_bytes    INTEGER NOT NULL,
    PRIMARY KEY (server_id, hour)
) STRICT;

CREATE TABLE servers_rollouts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id  INTEGER NOT NULL REFERENCES servers_templates(id),
    to_version   INTEGER NOT NULL,
    -- done: every item done or skipped; stopped: an item failed; cancelled: Stop rollout
    state        TEXT NOT NULL CHECK (state IN ('running','done','stopped','cancelled')),
    created_at   TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    finished_at  TEXT                        -- set with server.rollout_finished; a cancelled rollout whose item still runs has none yet
) STRICT;

CREATE TABLE servers_rollout_items (
    rollout_id   INTEGER NOT NULL REFERENCES servers_rollouts(id) ON DELETE CASCADE,
    position     INTEGER NOT NULL,
    server_id    INTEGER NOT NULL REFERENCES servers_servers(id),
    from_version INTEGER NOT NULL,
    params       TEXT NOT NULL DEFAULT '{}', -- values asked before the start
    state        TEXT NOT NULL CHECK (state IN ('waiting','running','done','failed','skipped')),
    job_id       INTEGER,
    error        TEXT,
    PRIMARY KEY (rollout_id, position)
) STRICT;

CREATE TABLE servers_agent_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id   INTEGER NOT NULL REFERENCES servers_templates(id) ON DELETE CASCADE,
    token_lookup  BLOB NOT NULL UNIQUE,      -- vault.Lookup(token)
    problem       TEXT NOT NULL,
    context       TEXT NOT NULL DEFAULT '{}',-- JSON: what was included (report, job id, server ids, diff)
    agent         TEXT NOT NULL CHECK (agent IN ('claude-code','codex','other')),
    opened_at     TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    closed_at     TEXT,
    close_reason  TEXT CHECK (close_reason IN ('expired','revoked','published','discarded','replaced')),
    saves         INTEGER NOT NULL DEFAULT 0,
    validations   INTEGER NOT NULL DEFAULT 0,
    requests      INTEGER NOT NULL DEFAULT 0,
    minute_start  TEXT,
    minute_count  INTEGER NOT NULL DEFAULT 0
) STRICT;
-- One open session per template (its draft).
CREATE UNIQUE INDEX servers_agent_sessions_open ON servers_agent_sessions(template_id) WHERE closed_at IS NULL;

-- +goose Down
DROP TABLE servers_agent_sessions;
DROP TABLE servers_rollout_items;
DROP TABLE servers_rollouts;
DROP TABLE servers_metric_hourly;
DROP TABLE servers_metric_samples;
DROP TABLE servers_checkhost_nodes;
DROP TABLE servers_home;
DROP TABLE servers_reference_results;
DROP TABLE servers_check_results;
DROP TABLE servers_dns_records;
DROP TABLE servers_deployed_files;
DROP TABLE servers_deployments;
DROP TABLE servers_endpoints;
DROP TABLE servers_generated_values;
DROP TABLE servers_servers;
DROP TABLE servers_template_files;
DROP TABLE servers_template_versions;
DROP TABLE servers_draft_files;
DROP TABLE servers_template_drafts;
DROP TABLE servers_templates;
DROP TABLE servers_locations;
DROP TABLE servers_meta;
