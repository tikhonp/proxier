-- +goose Up
ALTER TABLE servers_servers ADD COLUMN ssh_ip TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE servers_servers DROP COLUMN ssh_ip;
