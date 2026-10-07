-- +goose Up

CREATE TABLE jobs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    queue               TEXT NOT NULL,
    type                TEXT NOT NULL,
    resource_key        TEXT NOT NULL DEFAULT '',   -- "server:12"
    coalescing_key      TEXT NOT NULL DEFAULT '',
    subject_type        TEXT NOT NULL DEFAULT '',   -- what the Job page links to
    subject_id          TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'interrupted')),
    payload             TEXT NOT NULL DEFAULT '{}' CHECK (length(CAST(payload AS BLOB)) <= 65536), -- JSON, never secrets
    secrets             BLOB,                       -- vault, AAD "job:<id>:payload"
    quiet               INTEGER NOT NULL DEFAULT 0 CHECK (quiet IN (0, 1)), -- Type.Quiet at enqueue
    merged              INTEGER NOT NULL DEFAULT 0,
    attempt             INTEGER NOT NULL DEFAULT 0, -- counted attempts so far
    max_attempts        INTEGER NOT NULL CHECK (max_attempts >= 1),
    run_after           TEXT NOT NULL,
    boot_id             TEXT NOT NULL DEFAULT '',
    lease_until         TEXT,
    cancel_requested_at TEXT,
    cancel_requested_by TEXT,
    created_by          TEXT NOT NULL,              -- admin | cli | system | schedule:<name> | event:<id> | job:<id>
    retry_of            INTEGER REFERENCES jobs (id) ON DELETE SET NULL,
    created_at          TEXT NOT NULL,
    started_at          TEXT,                       -- first claim
    finished_at         TEXT,
    error               TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 4100),
    error_step          TEXT NOT NULL DEFAULT '',
    log_lines           INTEGER NOT NULL DEFAULT 0, -- written, including dropped
    log_dropped         INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX jobs_claim ON jobs (queue, state, run_after, id);
CREATE INDEX jobs_running_resource ON jobs (resource_key) WHERE state = 'running' AND resource_key != '';
CREATE UNIQUE INDEX jobs_coalesce ON jobs (type, coalescing_key) WHERE coalescing_key != '' AND state = 'queued' AND attempt = 0;
CREATE INDEX jobs_finished ON jobs (state, finished_at);
-- Clean quiet jobs are pruned after a day.
CREATE INDEX jobs_quiet ON jobs (finished_at) WHERE quiet = 1 AND state = 'succeeded';
CREATE INDEX jobs_subject ON jobs (subject_type, subject_id, id);

CREATE TABLE job_steps (
    job_id       INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    idx          INTEGER NOT NULL,
    name         TEXT NOT NULL,
    state        TEXT NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt      INTEGER NOT NULL DEFAULT 0,
    started_at   TEXT,
    finished_at  TEXT,
    error        TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 4100),
    carried_from INTEGER,                           -- succeeded in that job (a retry)
    PRIMARY KEY (job_id, idx),
    UNIQUE (job_id, name)
) STRICT;

CREATE TABLE job_log_lines (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,      -- SSE event id
    job_id  INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq     INTEGER NOT NULL,                       -- 1… per job, counting dropped lines
    time    TEXT NOT NULL,
    level   TEXT NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error', 'step', 'marker')),
    step    TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL,
    text    TEXT NOT NULL CHECK (length(text) <= 4100), -- already redacted, cut at 4,000
    UNIQUE (job_id, seq)
) STRICT;
CREATE INDEX job_log_lines_job ON job_log_lines (job_id, id);

-- Dispatcher cursors, one per subscriber.
CREATE TABLE event_cursors (
    subscriber    TEXT PRIMARY KEY,
    last_event_id INTEGER NOT NULL,
    updated_at    TEXT NOT NULL,
    failing_since TEXT,
    last_error    TEXT NOT NULL DEFAULT ''
) STRICT;

-- Run-time state of declared schedules.
CREATE TABLE schedules (
    name            TEXT PRIMARY KEY,               -- "platform.backup"
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    next_run_at     TEXT NOT NULL,
    last_run_at     TEXT,
    last_job_id     INTEGER,
    last_skipped_at TEXT,
    skipped         INTEGER NOT NULL DEFAULT 0
) STRICT;

ALTER TABLE admin ADD COLUMN jobs_seen_at TEXT;

-- +goose Down
ALTER TABLE admin DROP COLUMN jobs_seen_at;
DROP TABLE schedules;
DROP TABLE event_cursors;
DROP TABLE job_log_lines;
DROP TABLE job_steps;
DROP TABLE jobs;
