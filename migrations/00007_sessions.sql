-- +goose Up
-- Browser sessions, each made from an API token. Only a SHA-256 of the
-- cookie value is stored.
CREATE TABLE sessions (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_id     bigint NOT NULL REFERENCES api_tokens (id),
    session_hash bytea NOT NULL UNIQUE CHECK (length(session_hash) = 32),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
