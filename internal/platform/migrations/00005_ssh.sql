-- +goose Up

-- Proxier's own SSH key pair. One row.
CREATE TABLE ssh_identity (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    private_key BLOB NOT NULL,   -- vault, AAD "ssh:identity"; OpenSSH PEM
    public_key  TEXT NOT NULL,   -- authorized_keys line
    fingerprint TEXT NOT NULL,   -- "SHA256:…"
    created_at  TEXT NOT NULL
) STRICT;

-- Pinned host keys, one per address. A changed key waits in pending_* until
-- the admin accepts it or forgets the host.
CREATE TABLE known_hosts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    address             TEXT NOT NULL UNIQUE,   -- "host:port"
    subject             TEXT NOT NULL DEFAULT '', -- "server:12", "router:3", "jump:parents-pi"
    key_type            TEXT NOT NULL,
    public_key          BLOB NOT NULL,          -- wire format
    fingerprint         TEXT NOT NULL,
    first_seen_at       TEXT NOT NULL,
    accepted_at         TEXT NOT NULL,
    last_used_at        TEXT,
    pending_key_type    TEXT,
    pending_public_key  BLOB,
    pending_fingerprint TEXT,
    pending_seen_at     TEXT,
    CHECK ((pending_public_key IS NULL) = (pending_fingerprint IS NULL))
) STRICT;

-- +goose Down
DROP TABLE known_hosts;
DROP TABLE ssh_identity;
