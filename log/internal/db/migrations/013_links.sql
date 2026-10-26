-- Useful links (menu → Links): a title and a URL each, like the studio's
-- firing form or where to order clay.
CREATE TABLE links (
  id         INTEGER PRIMARY KEY,
  title      TEXT NOT NULL,
  url        TEXT NOT NULL,
  created_at TEXT NOT NULL
);
