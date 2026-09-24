-- Returns a row only if the key was free (new, expired, or abandoned).
-- name: BeginIdempotencyKey :one
INSERT INTO idempotency_keys (user_handle, key, request_hash)
VALUES (sqlc.arg(user_handle), sqlc.arg(key), sqlc.arg(request_hash))
ON CONFLICT (user_handle, key) DO UPDATE
SET request_hash = EXCLUDED.request_hash, status = NULL, headers = NULL, body = NULL, created_at = now()
WHERE idempotency_keys.created_at < now() - make_interval(secs => sqlc.arg(ttl_secs)::float8)
   OR (idempotency_keys.status IS NULL
       AND idempotency_keys.created_at < now() - make_interval(secs => sqlc.arg(stale_secs)::float8))
RETURNING user_handle;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE user_handle = $1 AND key = $2;

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys SET status = $3, headers = $4, body = $5
WHERE user_handle = $1 AND key = $2;

-- name: ReleaseIdempotencyKey :exec
DELETE FROM idempotency_keys
WHERE user_handle = $1 AND key = $2 AND status IS NULL;

-- name: DeleteExpiredIdempotencyKeys :exec
DELETE FROM idempotency_keys
WHERE created_at < now() - make_interval(secs => sqlc.arg(ttl_secs)::float8);
