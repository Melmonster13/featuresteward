-- +goose Up
-- SDK keys let an app evaluate flags in one environment and do nothing else.
CREATE TABLE sdk_keys (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    environment text NOT NULL REFERENCES environments (key),
    name        text NOT NULL,
    key_hash    bytea NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    prefix      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);

-- +goose Down
DROP TABLE sdk_keys;
