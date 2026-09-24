-- +goose Up
-- The steward is the user accountable for a flag. NULL means unassigned
-- (flags created before stewards existed). The API checks the handle
-- belongs to an active editor or above; handles are never deleted.
ALTER TABLE flags ADD COLUMN steward text;
CREATE INDEX flags_steward_idx ON flags (steward);

-- +goose Down
ALTER TABLE flags DROP COLUMN steward;
