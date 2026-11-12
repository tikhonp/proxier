-- +goose Up
-- More than one address on a server: one stack, several inbounds. The primary
-- address (IP, management and proxy hostname) stays on servers_servers; these
-- are the additional ones, each with its own IP and client-facing hostname.
-- Proxier does not write or check their DNS records: the member manages them.
CREATE TABLE servers_addresses (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id  INTEGER NOT NULL REFERENCES servers_servers(id),
    position   INTEGER NOT NULL CHECK (position >= 2), -- 1 is the primary address
    ip         TEXT NOT NULL,
    hostname   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (server_id, position),
    UNIQUE (server_id, ip),
    UNIQUE (server_id, hostname)
) STRICT;
CREATE INDEX servers_addresses_ip ON servers_addresses(ip);
CREATE INDEX servers_addresses_hostname ON servers_addresses(hostname);

-- +goose Down
DROP TABLE servers_addresses;