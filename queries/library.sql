-- name: LibraryTopics :many
SELECT id, title, is_public, share_slug, created_at
FROM topics
WHERE user_id = @user_id AND deleted_at IS NULL
ORDER BY created_at;

-- name: LibrarySources :many
SELECT id, topic_id, url, title, site
FROM sources
WHERE user_id = @user_id AND deleted_at IS NULL
  AND (sqlc.narg(topic_id)::uuid IS NULL OR topic_id = sqlc.narg(topic_id))
ORDER BY created_at;

-- name: LibraryHighlights :many
SELECT h.id, h.source_id, h.text, h.section, h.color, h.created_at
FROM highlights h
JOIN sources s ON s.id = h.source_id
WHERE h.user_id = @user_id AND h.deleted_at IS NULL
  AND (sqlc.narg(topic_id)::uuid IS NULL OR s.topic_id = sqlc.narg(topic_id))
ORDER BY h.created_at;

-- name: SharedTopic :one
-- Only what a share page shows: no email, no ids beyond the topic's.
SELECT t.id, t.user_id, t.title, u.name AS author
FROM topics t
JOIN users u ON u.id = t.user_id
WHERE t.share_slug = @slug AND t.is_public AND t.deleted_at IS NULL;

-- name: SetTopicShare :one
-- Sharing keeps the topic's link if it has one; going private clears it, so a
-- link once handed out stays dead even after sharing again.
UPDATE topics
SET is_public  = @is_public::boolean,
    share_slug = CASE WHEN @is_public::boolean THEN coalesce(share_slug, @new_slug) END
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING is_public, share_slug;

-- name: ListDevices :many
-- A device's session is its one live refresh token; refreshing replaces the row,
-- so created_at is when the device was last active.
SELECT id, device_info, created_at
FROM refresh_tokens
WHERE user_id = @user_id AND revoked_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;

-- name: RevokeDevice :execrows
UPDATE refresh_tokens
SET revoked_at = now()
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL;
