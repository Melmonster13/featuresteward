-- name: CreateFlag :one
INSERT INTO flags (key, name, description, steward)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateFlagEnvironments :exec
INSERT INTO flag_environments (flag_id, environment)
SELECT $1, key FROM environments;

-- name: GetFlag :one
SELECT * FROM flags WHERE key = $1;

-- name: GetFlagForUpdate :one
SELECT * FROM flags WHERE key = $1 FOR UPDATE;

-- name: ListFlags :many
SELECT * FROM flags WHERE archived_at IS NULL ORDER BY key;

-- name: ListFlagEnvironments :many
SELECT * FROM flag_environments WHERE flag_id = ANY(sqlc.arg(flag_ids)::bigint[]);

-- name: GetFlagEnvironmentForUpdate :one
SELECT * FROM flag_environments
WHERE flag_id = $1 AND environment = $2
FOR UPDATE;

-- name: UpdateFlag :one
UPDATE flags SET name = $2, description = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetSteward :one
UPDATE flags SET steward = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: TouchFlag :exec
UPDATE flags SET updated_at = now() WHERE id = $1;

-- name: UpdateFlagEnvironment :exec
UPDATE flag_environments
SET enabled = $3, rollout_percentage = $4, rules = $5, updated_at = now()
WHERE flag_id = $1 AND environment = $2;

-- name: ArchiveFlag :exec
UPDATE flags SET archived_at = now(), updated_at = now() WHERE id = $1;

-- name: GetEvalConfig :one
SELECT fe.enabled, fe.rollout_percentage, fe.rules
FROM flags f
JOIN flag_environments fe ON fe.flag_id = f.id
WHERE f.key = $1 AND fe.environment = $2 AND f.archived_at IS NULL;

-- name: ListEnvironments :many
SELECT * FROM environments ORDER BY key;

-- name: GetEnvironment :one
SELECT * FROM environments WHERE key = $1;

-- name: GetEnvironmentForUpdate :one
SELECT * FROM environments WHERE key = $1 FOR UPDATE;

-- name: CreateEnvironment :one
INSERT INTO environments (key, name, protected)
VALUES ($1, $2, $3)
RETURNING *;

-- Gives every existing flag (archived too) default settings in a new environment.
-- name: BackfillFlagEnvironments :exec
INSERT INTO flag_environments (flag_id, environment)
SELECT id, $1 FROM flags;

-- name: UpdateEnvironmentSettings :one
UPDATE environments SET name = $2, protected = $3
WHERE key = $1
RETURNING *;

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (actor, action, flag_key, environment, before, after)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAuditEvents :many
SELECT * FROM audit_events WHERE flag_key = $1 ORDER BY id;

-- name: SetPermanent :one
UPDATE flags SET permanent_reason = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- Keeps the latest time; unknown flags and environments match nothing.
-- name: RecordEvaluations :exec
UPDATE flag_environments fe
SET last_evaluated_at = GREATEST(fe.last_evaluated_at, u.at)
FROM (SELECT unnest(sqlc.arg(keys)::text[]) AS key,
             unnest(sqlc.arg(envs)::text[]) AS env,
             unnest(sqlc.arg(ats)::timestamptz[]) AS at) u,
     flags f
WHERE f.id = fe.flag_id AND f.key = u.key AND fe.environment = u.env;

-- name: MarkStaleNotified :exec
UPDATE flags SET stale_notified_at = sqlc.arg(at)
WHERE key = ANY(sqlc.arg(keys)::text[]);

-- name: CreateJob :exec
INSERT INTO jobs (name, last_run_at) VALUES ($1, '-infinity')
ON CONFLICT (name) DO NOTHING;

-- Claims a job's run if its last one was at least every ago. Only one
-- caller per period gets a row back.
-- name: ReleaseJob :exec
UPDATE jobs SET last_run_at = '-infinity' WHERE name = $1;

-- name: ClaimJob :one
UPDATE jobs SET last_run_at = now()
WHERE name = sqlc.arg(name) AND last_run_at <= now() - sqlc.arg(every)::interval
RETURNING name;
