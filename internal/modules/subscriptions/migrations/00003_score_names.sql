-- +goose Up
ALTER TABLE subs_subscriptions ADD COLUMN score_names TEXT NOT NULL DEFAULT '' CHECK (score_names IN ('', 'ip', 'claude', 'chatgpt'));

-- +goose Down
ALTER TABLE subs_subscriptions DROP COLUMN score_names;
