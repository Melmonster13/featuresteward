-- name: InsertChangeRequest :one
INSERT INTO change_requests (flag_id, environment, requested_by, reason, base, proposed, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: GetChangeRequest :one
SELECT cr.*, f.key AS flag_key
FROM change_requests cr JOIN flags f ON f.id = cr.flag_id
WHERE cr.id = $1;

-- name: GetChangeRequestForUpdate :one
SELECT cr.*, f.key AS flag_key
FROM change_requests cr JOIN flags f ON f.id = cr.flag_id
WHERE cr.id = $1
FOR UPDATE OF cr;

-- name: ListChangeRequests :many
SELECT cr.*, f.key AS flag_key
FROM change_requests cr JOIN flags f ON f.id = cr.flag_id
WHERE (sqlc.narg(status)::text IS NULL OR cr.status = sqlc.narg(status))
  AND (sqlc.narg(flag_key)::text IS NULL OR f.key = sqlc.narg(flag_key))
ORDER BY cr.id DESC
LIMIT 500;

-- name: ResolveChangeRequest :execrows
UPDATE change_requests
SET status = $2, reviewed_by = $3, review_comment = $4, resolved_at = now()
WHERE id = $1 AND status = 'pending';

-- Expires pending requests past their expiry, optionally for one flag and
-- environment, and returns them for auditing.
-- name: ExpireChangeRequests :many
WITH expired AS (
    UPDATE change_requests cr
    SET status = 'expired', resolved_at = now()
    WHERE cr.status = 'pending' AND cr.expires_at <= now()
      AND (sqlc.narg(flag_id)::bigint IS NULL OR cr.flag_id = sqlc.narg(flag_id))
      AND (sqlc.narg(environment)::text IS NULL OR cr.environment = sqlc.narg(environment))
    RETURNING cr.*
)
SELECT e.id, e.requested_by, e.reason, e.proposed, e.environment, f.key AS flag_key
FROM expired e JOIN flags f ON f.id = e.flag_id
ORDER BY e.id;
