-- +goose Up

-- Every subscription and link has an owner: a member (no foreign key, the
-- members are the platform's). What existed before members is member 1's.
-- The default only fills the rows already there: every insert names its
-- owner.
ALTER TABLE subs_subscriptions ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
ALTER TABLE subs_links ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0);
CREATE INDEX subs_subscriptions_owner ON subs_subscriptions(owner_id);
CREATE INDEX subs_links_owner ON subs_links(owner_id);

-- +goose Down

DROP INDEX subs_links_owner;
DROP INDEX subs_subscriptions_owner;
ALTER TABLE subs_links DROP COLUMN owner_id;
ALTER TABLE subs_subscriptions DROP COLUMN owner_id;
