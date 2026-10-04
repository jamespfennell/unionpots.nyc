CREATE TABLE projects (
  id          INTEGER PRIMARY KEY,
  name        TEXT,
  notes       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- Single row. Piece IDs are written on the pots, so they are never reused.
CREATE TABLE id_sequence (next_piece_id INTEGER NOT NULL);
INSERT INTO id_sequence (next_piece_id) VALUES (120);

CREATE TABLE pieces (
  id             INTEGER PRIMARY KEY,
  project_id     INTEGER NOT NULL REFERENCES projects(id),
  name           TEXT,
  notes          TEXT NOT NULL DEFAULT '',
  state          TEXT NOT NULL,               -- derived from the latest event
  public         INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX pieces_project ON pieces(project_id);
CREATE INDEX pieces_state ON pieces(state);

-- What happened to a piece and when. The piece's state is what it is
-- waiting for after its latest action (see internal/model).
CREATE TABLE events (
  id           INTEGER PRIMARY KEY,
  piece_id     INTEGER NOT NULL REFERENCES pieces(id) ON DELETE CASCADE,
  action       TEXT NOT NULL CHECK (action IN ('thrown','built','trimmed','queued_bisque','glazed','finished','broken')),
  occurred_on  TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX events_piece ON events(piece_id);

CREATE TABLE metadata (
  project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
  piece_id    INTEGER REFERENCES pieces(id)   ON DELETE CASCADE,
  key         TEXT NOT NULL,
  value       TEXT,
  updated_at  TEXT NOT NULL,
  CHECK ((project_id IS NULL) != (piece_id IS NULL))
);
CREATE UNIQUE INDEX metadata_project_key ON metadata(project_id, key) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX metadata_piece_key   ON metadata(piece_id, key)   WHERE piece_id   IS NOT NULL;
CREATE INDEX metadata_key ON metadata(key);

CREATE TABLE clays  (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, notes TEXT NOT NULL DEFAULT '');
CREATE TABLE glazes (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, notes TEXT NOT NULL DEFAULT '');
CREATE TABLE forms  (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, notes TEXT NOT NULL DEFAULT '');

CREATE TABLE piece_transfers (
  id           INTEGER PRIMARY KEY,
  piece_id     INTEGER NOT NULL REFERENCES pieces(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('kept','gifted','sold','other')),
  recipient    TEXT NOT NULL DEFAULT '',
  occurred_on  TEXT NOT NULL,
  note         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX piece_transfers_piece ON piece_transfers(piece_id);

CREATE TABLE photos (
  id            INTEGER PRIMARY KEY,
  sha256        TEXT NOT NULL UNIQUE,
  project_id    INTEGER REFERENCES projects(id) ON DELETE SET NULL,
  piece_id      INTEGER REFERENCES pieces(id)   ON DELETE SET NULL,
  state         TEXT,
  caption       TEXT NOT NULL DEFAULT '',
  width         INTEGER NOT NULL,
  height        INTEGER NOT NULL,
  original_ext  TEXT NOT NULL,
  public        INTEGER NOT NULL DEFAULT 0,
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  backed_up_at  TEXT
);
CREATE INDEX photos_piece ON photos(piece_id);
CREATE INDEX photos_project ON photos(project_id);

CREATE VIEW effective_metadata AS
  SELECT p.id AS piece_id, m.key, m.value, 0 AS inherited
    FROM pieces p JOIN metadata m ON m.piece_id = p.id
  UNION ALL
  SELECT p.id, m.key, m.value, 1
    FROM pieces p JOIN metadata m ON m.project_id = p.project_id
   WHERE NOT EXISTS (SELECT 1 FROM metadata o WHERE o.piece_id = p.id AND o.key = m.key);
