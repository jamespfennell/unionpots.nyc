-- Where a piece is made. Every piece so far was made at Clayworks.
CREATE TABLE studios (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
  created_at TEXT NOT NULL
);
ALTER TABLE pieces ADD COLUMN studio_id INTEGER REFERENCES studios(id);
INSERT INTO studios (name, created_at)
  SELECT 'Clayworks', strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE EXISTS (SELECT 1 FROM pieces);
UPDATE pieces SET studio_id = (SELECT id FROM studios WHERE name = 'Clayworks');
