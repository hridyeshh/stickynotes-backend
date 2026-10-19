-- name: UpsertGoogleUser :one
-- Google's sub is the identity; email, name and picture refresh on every sign-in.
INSERT INTO users (google_sub, email, name, avatar_url)
VALUES ($1, $2, $3, $4)
ON CONFLICT (google_sub) DO UPDATE
SET email = EXCLUDED.email, name = EXCLUDED.name, avatar_url = EXCLUDED.avatar_url
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: SetUserAvatar :exec
UPDATE users SET avatar = $2 WHERE id = $1;

-- name: SetUserSuggestions :exec
UPDATE users SET suggestions = @suggestions::boolean WHERE id = @id;

-- name: DeleteUser :exec
-- Cascades to every token, topic, source and highlight.
DELETE FROM users WHERE id = $1;
