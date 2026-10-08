-- stickynotes schema: users and auth, then topics → sources → highlights.

CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Google's stable account id; email can change, sub never does.
    google_sub TEXT NOT NULL UNIQUE,
    email      TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One live row per signed-in device; rotated on every refresh.
CREATE TABLE refresh_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    device_info JSONB NOT NULL DEFAULT '{}',
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_live_idx ON refresh_tokens (user_id) WHERE revoked_at IS NULL;

-- Single-use PKCE codes that hand a web session over to the browser extension.
CREATE TABLE extension_codes (
    code_hash      TEXT PRIMARY KEY,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_challenge TEXT NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ
);

-- A topic is the note the user sees ("Consistent hashing"). Ids come from the client.
CREATE TABLE topics (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 300),
    is_public  BOOLEAN NOT NULL DEFAULT false,
    share_slug TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
-- The same title created on two offline devices must fold into one topic.
CREATE UNIQUE INDEX topics_user_title_key ON topics (user_id, lower(title)) WHERE deleted_at IS NULL;
CREATE INDEX topics_user_updated_idx ON topics (user_id, updated_at);

-- One row per URL saved from. Belongs to exactly one topic.
CREATE TABLE sources (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id   UUID NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    -- http(s) only: these URLs are rendered as links on public share pages.
    url        TEXT NOT NULL CHECK (url ~ '^https?://' AND char_length(url) <= 2048),
    title      TEXT NOT NULL DEFAULT '' CHECK (char_length(title) <= 300),
    site       TEXT NOT NULL DEFAULT '' CHECK (char_length(site) <= 255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    UNIQUE (user_id, url)
);
CREATE INDEX sources_topic_idx ON sources (topic_id);
CREATE INDEX sources_user_updated_idx ON sources (user_id, updated_at);

-- A saved passage. Deleting blanks the text and sets deleted_at, so devices sync the removal.
CREATE TABLE highlights (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_id  UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    text       TEXT NOT NULL CHECK (char_length(text) <= 10000),
    section    TEXT NOT NULL DEFAULT '' CHECK (char_length(section) <= 300),
    anchor     JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX highlights_source_idx ON highlights (source_id);
CREATE INDEX highlights_user_updated_idx ON highlights (user_id, updated_at);

CREATE TRIGGER users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER topics_updated_at BEFORE UPDATE ON topics FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER sources_updated_at BEFORE UPDATE ON sources FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER highlights_updated_at BEFORE UPDATE ON highlights FOR EACH ROW EXECUTE FUNCTION set_updated_at();
