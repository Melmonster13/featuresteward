-- +goose Up
-- Remembers responses to state-changing requests sent with an
-- Idempotency-Key header, so a retry replays the result instead of
-- applying the change twice. Rows expire after a day.
CREATE TABLE idempotency_keys (
    user_handle  text NOT NULL,
    key          text NOT NULL,
    request_hash bytea NOT NULL,
    status       integer,          -- NULL while the first request is in progress
    headers      jsonb,
    body         bytea,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_handle, key)
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
