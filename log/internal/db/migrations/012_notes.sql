-- General notes (menu → Notes): a title and free text each, listed most
-- recently edited first.
CREATE TABLE notes (
  id         INTEGER PRIMARY KEY,
  title      TEXT NOT NULL,
  text       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
