-- name: CreateFlag :one
INSERT INTO flags (key, name, description)
VALUES ($1, $2, $3)
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

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (actor, action, flag_key, environment, before, after)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAuditEvents :many
SELECT * FROM audit_events WHERE flag_key = $1 ORDER BY id;
