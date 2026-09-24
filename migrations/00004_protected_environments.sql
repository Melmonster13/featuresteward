-- +goose Up
-- Changes to a protected environment's flags need an admin (and, once
-- approvals exist, a second person).
ALTER TABLE environments ADD COLUMN protected boolean NOT NULL DEFAULT false;
UPDATE environments SET protected = true WHERE key = 'prod';

-- +goose Down
ALTER TABLE environments DROP COLUMN protected;
