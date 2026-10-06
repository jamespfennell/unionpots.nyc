-- Free text kept by the app itself: for now just "ideas", the page of ideas
-- for improving the log (menu → Ideas).
CREATE TABLE app_text (
  name       TEXT PRIMARY KEY,
  text       TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
