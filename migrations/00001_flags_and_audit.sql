-- +goose Up
CREATE TABLE environments (
    key        text PRIMARY KEY CHECK (key ~ '^[a-z0-9-]+$'),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO environments (key, name) VALUES
    ('dev', 'Development'),
    ('staging', 'Staging'),
    ('prod', 'Production');

CREATE TABLE flags (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key         text NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9][a-z0-9-]*$'),
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);

CREATE TABLE flag_environments (
    flag_id            bigint NOT NULL REFERENCES flags (id) ON DELETE CASCADE,
    environment        text NOT NULL REFERENCES environments (key),
    enabled            boolean NOT NULL DEFAULT false,
    rollout_percentage smallint NOT NULL DEFAULT 100 CHECK (rollout_percentage BETWEEN 0 AND 100),
    rules              jsonb NOT NULL DEFAULT '[]',
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (flag_id, environment)
);

CREATE TABLE audit_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor       text NOT NULL,
    action      text NOT NULL,
    flag_key    text,
    environment text,
    before      jsonb,
    after       jsonb
);

CREATE INDEX audit_events_flag_key_idx ON audit_events (flag_key, occurred_at);

-- +goose StatementBegin
CREATE FUNCTION audit_events_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER audit_events_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

CREATE TRIGGER audit_events_no_truncate
    BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_append_only();

-- +goose Down
DROP TABLE audit_events;
DROP FUNCTION audit_events_append_only();
DROP TABLE flag_environments;
DROP TABLE flags;
DROP TABLE environments;
