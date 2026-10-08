-- name: CreateExtensionCode :exec
INSERT INTO extension_codes (code_hash, user_id, code_challenge, expires_at)
VALUES ($1, $2, $3, $4);

-- name: ConsumeExtensionCode :one
-- Use-and-return in one statement, so a code can be exchanged only once even when
-- two requests race.
UPDATE extension_codes
SET used_at = now()
WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING user_id, code_challenge;
