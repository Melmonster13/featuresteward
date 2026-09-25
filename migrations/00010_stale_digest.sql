-- +goose Up
-- When the flag's steward was last told it's stale, so reminders repeat
-- at most weekly.
ALTER TABLE flags ADD COLUMN stale_notified_at timestamptz;

-- Scheduled jobs that must run once per period across all servers.
CREATE TABLE jobs (
    name        text PRIMARY KEY,
    last_run_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE jobs;
ALTER TABLE flags DROP COLUMN stale_notified_at;
