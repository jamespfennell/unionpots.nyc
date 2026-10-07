-- App ideas become a list. Each idea has a random hash (never shown in the
-- app): implementing an idea lists its hash in internal/ideas/done.txt, and
-- the app marks it done on startup. A "simple" idea can be implemented
-- straight away without discussion (see log/CLAUDE.md).
CREATE TABLE ideas (
  id         INTEGER PRIMARY KEY,
  hash       TEXT NOT NULL UNIQUE,
  text       TEXT NOT NULL,
  simple     INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  done_at    TEXT
);

-- The free-text ideas so far become one idea per non-blank line, in order,
-- without a leading "- " or "* ".
WITH RECURSIVE split(line, rest) AS (
  SELECT '', (SELECT text FROM app_text WHERE name = 'ideas') || char(10)
  UNION ALL
  SELECT substr(rest, 1, instr(rest, char(10)) - 1), substr(rest, instr(rest, char(10)) + 1)
  FROM split WHERE rest <> ''
),
lines(t) AS (
  SELECT trim(line, ' ' || char(9) || char(13)) FROM split
)
INSERT INTO ideas (hash, text, created_at)
SELECT lower(hex(randomblob(16))),
       CASE WHEN substr(t, 1, 2) IN ('- ', '* ') THEN trim(substr(t, 3)) ELSE t END,
       strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
FROM lines WHERE t <> '';

DELETE FROM app_text WHERE name = 'ideas';
