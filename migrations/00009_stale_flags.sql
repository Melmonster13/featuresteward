-- +goose Up
-- A reason here marks a flag as meant to last; it's never reported stale.
ALTER TABLE flags ADD COLUMN permanent_reason text;

-- When a flag was last evaluated in an environment. Rows that exist when
-- tracking starts, and new rows, start at now(), so no flag looks unused
-- only because it hasn't been tracked yet.
ALTER TABLE flag_environments ADD COLUMN last_evaluated_at timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE flag_environments DROP COLUMN last_evaluated_at;
ALTER TABLE flags DROP COLUMN permanent_reason;
