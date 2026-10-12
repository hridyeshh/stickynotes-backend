-- name: InsertTopic :exec
-- Does nothing when the id exists or the user already has a live topic with this
-- title; ResolveTopic then finds the row that won.
INSERT INTO topics (id, user_id, title)
VALUES (@id, @user_id, @title)
ON CONFLICT DO NOTHING;

-- name: ResolveTopic :one
-- The user's topic with this id, else their live topic with this title.
SELECT id FROM topics
WHERE user_id = @user_id AND (id = @id OR (lower(title) = lower(@title) AND deleted_at IS NULL))
ORDER BY id = @id DESC
LIMIT 1;

-- name: UpsertSource :one
-- One source per URL, attached only to a topic the user owns: no row comes back
-- otherwise. Saving to a deleted source revives it in the given topic; a live
-- source keeps its topic, because moving one is an explicit action.
INSERT INTO sources (user_id, topic_id, url, title, site)
SELECT t.user_id, t.id, @url::text, @title::text, @site::text
FROM topics t
WHERE t.id = @topic_id AND t.user_id = @user_id
ON CONFLICT (user_id, url) DO UPDATE
SET topic_id   = CASE WHEN sources.deleted_at IS NULL THEN sources.topic_id ELSE EXCLUDED.topic_id END,
    title      = EXCLUDED.title,
    site       = EXCLUDED.site,
    deleted_at = NULL
RETURNING id;

-- name: HighlightExists :one
-- Any user's: a highlight id is created once and never reused.
SELECT EXISTS (SELECT 1 FROM highlights WHERE id = @id);

-- name: InsertHighlight :exec
INSERT INTO highlights (id, user_id, source_id, text, section, anchor, color, created_at)
VALUES (@id, @user_id, @source_id, @text, @section, @anchor, @color, @created_at)
ON CONFLICT (id) DO NOTHING;

-- name: DeleteHighlight :one
-- Blanks the text, keeping the row as a deletion marker for the user's other devices.
UPDATE highlights
SET deleted_at = now(), text = '', anchor = NULL
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING source_id;

-- name: DeleteSourceIfEmpty :exec
-- Once its last highlight is gone, the source leaves its topic too.
UPDATE sources s
SET deleted_at = now()
WHERE s.id = @id AND s.user_id = @user_id AND s.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM highlights h WHERE h.source_id = s.id AND h.deleted_at IS NULL);

-- name: MoveSource :execrows
-- Moves a live source to another of the user's own topics.
UPDATE sources s
SET topic_id = t.id
FROM topics t
WHERE s.user_id = @user_id AND s.url = @url AND s.deleted_at IS NULL
  AND t.id = @topic_id AND t.user_id = @user_id;

-- name: TopicsSince :many
SELECT id, title, (deleted_at IS NOT NULL)::boolean AS deleted
FROM topics
WHERE user_id = @user_id AND updated_at > @since AND (deleted_at IS NULL OR @with_deleted::boolean);

-- name: SourcesSince :many
SELECT topic_id, url, title, site, (deleted_at IS NOT NULL)::boolean AS deleted
FROM sources
WHERE user_id = @user_id AND updated_at > @since AND (deleted_at IS NULL OR @with_deleted::boolean);

-- name: HighlightsSince :many
SELECT h.id, s.url, h.text, h.section, h.anchor, h.color, h.created_at, (h.deleted_at IS NOT NULL)::boolean AS deleted
FROM highlights h
JOIN sources s ON s.id = h.source_id
WHERE h.user_id = @user_id AND h.updated_at > @since AND (h.deleted_at IS NULL OR @with_deleted::boolean);

-- name: ReviveTopic :exec
-- A device that was offline may still save into a topic deleted or merged away
-- elsewhere. Bring it back rather than lose the note, unless a live topic now has
-- its title.
UPDATE topics t
SET deleted_at = NULL
WHERE t.id = @id AND t.user_id = @user_id AND t.deleted_at IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM topics o
    WHERE o.user_id = t.user_id AND lower(o.title) = lower(t.title) AND o.deleted_at IS NULL
  );

-- name: RenameTopic :execrows
UPDATE topics
SET title = @title
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL;

-- name: MoveTopicSources :exec
-- Merging: every live source of one topic joins another of the user's topics.
UPDATE sources s
SET topic_id = t.id
FROM topics t
WHERE s.topic_id = @from_id AND s.user_id = @user_id AND s.deleted_at IS NULL
  AND t.id = @into_id AND t.user_id = @user_id AND t.deleted_at IS NULL;

-- name: DeleteTopicHighlights :exec
UPDATE highlights h
SET deleted_at = now(), text = '', anchor = NULL
FROM sources s
WHERE h.source_id = s.id AND s.topic_id = @topic_id AND h.user_id = @user_id AND h.deleted_at IS NULL;

-- name: DeleteTopicSources :exec
UPDATE sources
SET deleted_at = now()
WHERE topic_id = @topic_id AND user_id = @user_id AND deleted_at IS NULL;

-- name: DeleteTopic :exec
-- Its share link dies with it.
UPDATE topics
SET deleted_at = now(), is_public = false, share_slug = NULL
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL;

-- name: TopicIsLive :one
SELECT EXISTS (SELECT 1 FROM topics WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL);
