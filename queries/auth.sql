-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (user_id, token_hash, device_info, expires_at)
VALUES ($1, $2, $3, $4);

-- name: ConsumeRefreshToken :one
-- Revoke-and-return in one statement, so a refresh token can be exchanged only
-- once even when two requests race.
UPDATE refresh_tokens
SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
RETURNING user_id, device_info;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL;
