-- Add the "started" action (added before throwing, or building over several
-- sessions). SQLite can't change a CHECK constraint, so the events table is
-- rebuilt with the longer list of actions.

CREATE TABLE events_new (
  id           INTEGER PRIMARY KEY,
  piece_id     INTEGER NOT NULL REFERENCES pieces(id) ON DELETE CASCADE,
  action       TEXT NOT NULL CHECK (action IN ('started','thrown','built','trimmed','queued_bisque','glazed','finished','broken')),
  occurred_on  TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
INSERT INTO events_new (id, piece_id, action, occurred_on, created_at)
  SELECT id, piece_id, action, occurred_on, created_at FROM events;
DROP TABLE events;
ALTER TABLE events_new RENAME TO events;
CREATE INDEX events_piece ON events(piece_id);
