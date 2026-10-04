-- Clay bodies are records of their own, with details, and pieces link to the
-- clay(s) they're made from.

ALTER TABLE clays ADD COLUMN code  TEXT NOT NULL DEFAULT '';  -- product / serial number
ALTER TABLE clays ADD COLUMN price TEXT NOT NULL DEFAULT '';  -- free text, e.g. "$38 / 25 lb"

CREATE TABLE piece_clays (
  piece_id  INTEGER NOT NULL REFERENCES pieces(id) ON DELETE CASCADE,
  clay_id   INTEGER NOT NULL REFERENCES clays(id),
  PRIMARY KEY (piece_id, clay_id)
);
CREATE INDEX piece_clays_clay ON piece_clays(clay_id);
