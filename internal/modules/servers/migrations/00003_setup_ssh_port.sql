-- +goose Up
-- ssh_port is the server's SSH port for its whole life, drawn at Create and
-- moved to by base bootstrap; setup_ssh_port is the VPS's stock one, which
-- preflight and install access use. A server made before both were kept had
-- one port for both.
ALTER TABLE servers_servers ADD COLUMN setup_ssh_port INTEGER NOT NULL DEFAULT 22;
UPDATE servers_servers SET setup_ssh_port = ssh_port;

-- +goose Down
ALTER TABLE servers_servers DROP COLUMN setup_ssh_port;
