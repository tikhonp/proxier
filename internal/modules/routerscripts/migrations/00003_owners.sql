-- +goose Up

-- Every generation has an owner: a member (no foreign key, the members are
-- the platform's). Its fetch URLs follow it. Generations made before members
-- are member 1's; the default only fills the rows already there: every
-- insert names its owner.
ALTER TABLE rscripts_generations ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
CREATE INDEX rscripts_generations_owner ON rscripts_generations(owner_id);

-- +goose Down

DROP INDEX rscripts_generations_owner;
ALTER TABLE rscripts_generations DROP COLUMN owner_id;
