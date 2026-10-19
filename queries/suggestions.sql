-- name: SuggestionsOn :one
SELECT coalesce(suggestions, false)::boolean FROM users WHERE id = @id;

-- name: SuggestionTopic :one
SELECT title, tags FROM topics WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL;

-- name: NewestTopicNotes :many
-- What a build reads besides the title. Notes are never edited, so their ids say when this changes.
SELECT h.id, h.text
FROM highlights h
JOIN sources s ON s.id = h.source_id
WHERE s.topic_id = @topic_id AND h.user_id = @user_id AND h.deleted_at IS NULL
ORDER BY h.created_at DESC, h.id
LIMIT 30;

-- name: TopicSuggestions :one
-- due: a day has passed since the build, so changed notes may rebuild it. Judged by the database's clock, like built_at.
SELECT notes_key, links, tags, (built_at < now() - interval '1 day')::boolean AS due
FROM topic_suggestions WHERE topic_id = @topic_id AND user_id = @user_id;

-- name: BuildsToday :one
SELECT count(*) FROM topic_suggestions WHERE user_id = @user_id AND built_at > now() - interval '1 day';

-- name: SaveTopicSuggestions :exec
-- Only while the user has suggestions on: a build still running when they turned them off saves nothing.
INSERT INTO topic_suggestions (topic_id, user_id, notes_key, links, tags)
SELECT @topic_id::uuid, u.id, @notes_key::text, @links::jsonb, @tags::text[]
FROM users u
WHERE u.id = @user_id AND u.suggestions
ON CONFLICT (topic_id) DO UPDATE
SET notes_key = EXCLUDED.notes_key, links = EXCLUDED.links, tags = EXCLUDED.tags, built_at = now()
WHERE topic_suggestions.user_id = EXCLUDED.user_id;

-- name: DeleteUserSuggestions :exec
DELETE FROM topic_suggestions WHERE user_id = @user_id;
