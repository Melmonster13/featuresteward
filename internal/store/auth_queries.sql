-- name: CreateUser :one
INSERT INTO users (handle, name, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE handle = $1;

-- name: GetUserForUpdate :one
SELECT * FROM users WHERE handle = $1 FOR UPDATE;

-- name: ListUsers :many
SELECT * FROM users ORDER BY handle;

-- name: SetUserRole :one
UPDATE users SET role = $2 WHERE id = $1 RETURNING *;

-- name: DisableUser :exec
UPDATE users SET disabled_at = now() WHERE id = $1;

-- name: CreateToken :one
INSERT INTO api_tokens (user_id, name, token_hash, prefix, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListTokens :many
SELECT * FROM api_tokens WHERE user_id = $1 ORDER BY id;

-- name: RevokeToken :one
UPDATE api_tokens SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeUserTokens :exec
UPDATE api_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: AuthenticateToken :one
SELECT u.*, t.id AS token_id
FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = $1
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now())
  AND u.disabled_at IS NULL;

-- Throttled so authentication isn't a write on every request.
-- name: TouchToken :exec
UPDATE api_tokens SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: InsertUserAuditEvent :exec
INSERT INTO audit_events (actor, action, subject_user, before, after)
VALUES ($1, $2, $3, $4, $5);

-- name: ListUserAuditEvents :many
SELECT * FROM audit_events WHERE subject_user = $1 ORDER BY id;
