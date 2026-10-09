-- +goose Up

-- The mtvpn import's preview reports each service list that couldn't be read
-- (3d): JSON [{"url":…,"error":…}]. Added after 3a's schema rather than
-- edited into 00001, which may already be deployed.
ALTER TABLE routing_imports ADD COLUMN list_failures TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE routing_imports DROP COLUMN list_failures;
