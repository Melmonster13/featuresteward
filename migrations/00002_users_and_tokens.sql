-- +goose Up
CREATE TABLE users (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    handle      text NOT NULL UNIQUE CHECK (handle ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    name        text NOT NULL DEFAULT '',
    role        text NOT NULL CHECK (role IN ('viewer', 'editor', 'approver', 'admin')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    disabled_at timestamptz
);

-- Only a SHA-256 of each token is stored; the token itself is shown once.
CREATE TABLE api_tokens (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id),
    name         text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    prefix       text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    last_used_at timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

ALTER TABLE audit_events ADD COLUMN subject_user text;
CREATE INDEX audit_events_subject_user_idx ON audit_events (subject_user, occurred_at);

-- +goose Down
DROP INDEX audit_events_subject_user_idx;
ALTER TABLE audit_events DROP COLUMN subject_user;
DROP TABLE api_tokens;
DROP TABLE users;
