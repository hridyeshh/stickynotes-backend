-- The avatar picked on the website: a key from its set (public/avatars), or '' until one is picked.
ALTER TABLE users ADD COLUMN avatar TEXT NOT NULL DEFAULT '';
