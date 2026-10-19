-- Tags on topics, and reading suggestions, which stay off until the user turns them on.
ALTER TABLE topics ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}' CHECK (cardinality(tags) <= 12);

-- NULL until the user decides: a topic's page offers suggestions only then.
ALTER TABLE users ADD COLUMN suggestions BOOLEAN;

-- What a topic's page suggests: pages to read next (Brave Search results for searches Gemini wrote from the topic's
-- title and newest notes) and tag ideas. Rebuilt when those notes change, at most once a day. Turning suggestions off
-- deletes the user's rows.
CREATE TABLE topic_suggestions (
    topic_id  UUID PRIMARY KEY REFERENCES topics(id) ON DELETE CASCADE,
    user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- A hash of the title and notes the build read.
    notes_key TEXT NOT NULL,
    links     JSONB NOT NULL DEFAULT '[]',
    tags      TEXT[] NOT NULL DEFAULT '{}',
    built_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX topic_suggestions_user_idx ON topic_suggestions (user_id, built_at);
