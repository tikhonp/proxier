-- +goose Up

-- A script's draft handed to a coding agent. Only the keyed hash of the
-- token is stored. Deleting the script deletes its sessions.
CREATE TABLE rscripts_agent_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    script_id     INTEGER NOT NULL REFERENCES rscripts_scripts(id) ON DELETE CASCADE,
    token_lookup  BLOB NOT NULL UNIQUE,                         -- vault.Lookup(token)
    problem       TEXT NOT NULL,
    context       TEXT NOT NULL DEFAULT '{}',                   -- JSON: what was included (diff)
    agent         TEXT NOT NULL CHECK (agent IN ('claude-code','codex','other')),
    opened_at     TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    closed_at     TEXT,
    close_reason  TEXT CHECK (close_reason IN ('expired','revoked','published','discarded','replaced')),
    saves         INTEGER NOT NULL DEFAULT 0,
    requests      INTEGER NOT NULL DEFAULT 0,
    minute_start  TEXT,
    minute_count  INTEGER NOT NULL DEFAULT 0,
    CHECK ((closed_at IS NULL) = (close_reason IS NULL))
) STRICT;
-- One open session per script (its draft).
CREATE UNIQUE INDEX rscripts_agent_sessions_open ON rscripts_agent_sessions(script_id) WHERE closed_at IS NULL;

-- +goose Down
DROP TABLE rscripts_agent_sessions;
