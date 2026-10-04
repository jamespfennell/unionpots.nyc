-- Glazes are written as free text ("Pink with dabs of Red"); known glaze
-- names (the glazes table) found in it are stored as the piece's glazes for
-- search. Existing glaze lists become known glazes, and each piece's list
-- and "how applied" note become one text.

INSERT OR IGNORE INTO glazes (name)
  SELECT DISTINCT trim(j.value)
  FROM metadata m, json_each(m.value) j
  WHERE m.key = 'glazes' AND m.piece_id IS NOT NULL AND trim(j.value) <> '';

INSERT INTO metadata (piece_id, key, value, updated_at)
SELECT piece_id, 'glaze_text', json_quote(txt), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM (
  SELECT p.id AS piece_id, trim(
      COALESCE((SELECT group_concat(j.value, ', ')
                FROM metadata g, json_each(g.value) j
                WHERE g.piece_id = p.id AND g.key = 'glazes'), '')
      || COALESCE(
           CASE WHEN EXISTS (SELECT 1 FROM metadata g WHERE g.piece_id = p.id AND g.key = 'glazes')
                THEN '. ' ELSE '' END
           || (SELECT json_extract(n.value, '$') FROM metadata n
               WHERE n.piece_id = p.id AND n.key = 'glaze_notes' AND json_extract(n.value, '$') <> ''),
           '')
    ) AS txt
  FROM pieces p
)
WHERE txt <> '';

DELETE FROM metadata WHERE key = 'glaze_notes';
