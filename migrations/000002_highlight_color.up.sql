-- The highlighter colour a note was saved in. Existing notes are yellow.
ALTER TABLE highlights
    ADD COLUMN color TEXT NOT NULL DEFAULT 'yellow' CHECK (color IN ('yellow', 'pink', 'blue', 'green'));
