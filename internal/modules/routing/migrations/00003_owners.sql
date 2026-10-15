-- +goose Up

-- Lists, routers, Shadowrocket configs, imports and discovery runs have an
-- owner: a member (no foreign key, the members are the platform's). What
-- existed before members is member 1's. The default only fills the rows
-- already there: every insert names its owner.
ALTER TABLE routing_lists ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
ALTER TABLE routing_routers ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
ALTER TABLE routing_shadowrocket ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
ALTER TABLE routing_imports ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
ALTER TABLE routing_discovery_runs ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
CREATE INDEX routing_lists_owner ON routing_lists(owner_id);
CREATE INDEX routing_routers_owner ON routing_routers(owner_id);
CREATE INDEX routing_shadowrocket_owner ON routing_shadowrocket(owner_id);
CREATE INDEX routing_imports_owner ON routing_imports(owner_id);
CREATE INDEX routing_discovery_runs_owner ON routing_discovery_runs(owner_id);
-- A connection test belongs to who started it: one from the add form has no
-- router whose owner decides who sees its host and fingerprints.
ALTER TABLE routing_tests ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);

-- A custom service is private to its owner; v2fly, iplist and url services
-- are shared, owner 0. ALTER TABLE can't add a check across two columns to
-- rows that don't meet it yet, so the triggers hold it.
ALTER TABLE routing_services ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 0 CHECK (owner_id >= 0);
UPDATE routing_services SET owner_id = 1 WHERE source = 'custom';
CREATE INDEX routing_services_owner ON routing_services(owner_id);
-- +goose StatementBegin
CREATE TRIGGER routing_services_owner_insert BEFORE INSERT ON routing_services
WHEN (NEW.source = 'custom') <> (NEW.owner_id > 0)
BEGIN
    SELECT RAISE(ABORT, 'routing_services: a custom service has an owner, a shared one has none');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER routing_services_owner_update BEFORE UPDATE OF source, owner_id ON routing_services
WHEN (NEW.source = 'custom') <> (NEW.owner_id > 0)
BEGIN
    SELECT RAISE(ABORT, 'routing_services: a custom service has an owner, a shared one has none');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER routing_services_owner_update;
DROP TRIGGER routing_services_owner_insert;
DROP INDEX routing_services_owner;
ALTER TABLE routing_services DROP COLUMN owner_id;
DROP INDEX routing_discovery_runs_owner;
DROP INDEX routing_imports_owner;
DROP INDEX routing_shadowrocket_owner;
DROP INDEX routing_routers_owner;
DROP INDEX routing_lists_owner;
ALTER TABLE routing_tests DROP COLUMN owner_id;
ALTER TABLE routing_discovery_runs DROP COLUMN owner_id;
ALTER TABLE routing_imports DROP COLUMN owner_id;
ALTER TABLE routing_shadowrocket DROP COLUMN owner_id;
ALTER TABLE routing_routers DROP COLUMN owner_id;
ALTER TABLE routing_lists DROP COLUMN owner_id;
