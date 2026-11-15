-- +goose Up
CREATE TABLE servers_ai_results (
    id INTEGER PRIMARY KEY,
    server_id INTEGER NOT NULL REFERENCES servers_servers(id),
    endpoint_key TEXT NOT NULL,
    generation TEXT NOT NULL,
    service TEXT NOT NULL CHECK (service IN ('claude', 'chatgpt', 'openai_api')),
    attempted_at TEXT NOT NULL,
    observed_at TEXT,
    ip TEXT NOT NULL DEFAULT '',
    family INTEGER NOT NULL DEFAULT 0,
    reachable INTEGER NOT NULL DEFAULT 0,
    http_status INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    lookup_at TEXT,
    raw_score REAL CHECK (raw_score BETWEEN 0 AND 100),
    country TEXT NOT NULL DEFAULT '',
    flags TEXT NOT NULL DEFAULT '{}'
) STRICT;
CREATE INDEX servers_ai_results_latest ON servers_ai_results(server_id, endpoint_key, generation, service, id DESC);
CREATE INDEX servers_ai_results_retention ON servers_ai_results(attempted_at);
CREATE TABLE servers_ai_cache (
    ip TEXT PRIMARY KEY,
    fetched_at TEXT NOT NULL,
    reputation TEXT NOT NULL
) STRICT;
CREATE TABLE servers_ai_backoff (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    until_at TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE servers_ai_backoff;
DROP TABLE servers_ai_cache;
DROP TABLE servers_ai_results;
