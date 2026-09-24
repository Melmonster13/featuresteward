-- +goose Up
-- Proposed flag changes that need a second person's approval.
CREATE TABLE change_requests (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    flag_id        bigint NOT NULL REFERENCES flags (id),
    environment    text NOT NULL REFERENCES environments (key),
    requested_by   text NOT NULL,
    reason         text NOT NULL DEFAULT '',
    -- The environment's config when the request was made, and the
    -- config to apply. Approval requires the environment to still match base.
    base           jsonb NOT NULL,
    proposed       jsonb NOT NULL,
    status         text NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired')),
    reviewed_by    text,
    review_comment text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    resolved_at    timestamptz,
    CHECK ((status = 'pending') = (resolved_at IS NULL))
);

-- At most one pending request per flag and environment.
CREATE UNIQUE INDEX change_requests_one_pending ON change_requests (flag_id, environment) WHERE status = 'pending';
CREATE INDEX change_requests_status_idx ON change_requests (status, id);

-- +goose Down
DROP TABLE change_requests;
