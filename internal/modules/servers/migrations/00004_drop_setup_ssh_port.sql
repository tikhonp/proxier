-- +goose Up
-- SSH stays on the port the VPS came with: provisioning no longer moves it,
-- so a server has one SSH port again. A server that failed before base
-- bootstrap never had sshd moved and takes its setup port back; every other
-- one keeps the port sshd was moved to.
UPDATE servers_servers SET ssh_port = setup_ssh_port
	WHERE state = 'failed' AND failed_step IN ('preflight', 'install-access');
ALTER TABLE servers_servers DROP COLUMN setup_ssh_port;

-- +goose Down
ALTER TABLE servers_servers ADD COLUMN setup_ssh_port INTEGER NOT NULL DEFAULT 22;
UPDATE servers_servers SET setup_ssh_port = ssh_port;
