-- Every name is a project name. A lone piece's name moves up to its project;
-- any other piece names are kept by moving them into the piece's notes.

UPDATE projects
   SET name = (SELECT name FROM pieces WHERE project_id = projects.id)
 WHERE name IS NULL
   AND (SELECT COUNT(*) FROM pieces WHERE project_id = projects.id) = 1
   AND (SELECT name FROM pieces WHERE project_id = projects.id) IS NOT NULL;

UPDATE pieces
   SET notes = 'Name: ' || name || CASE WHEN notes = '' THEN '' ELSE char(10) || notes END
 WHERE name IS NOT NULL
   AND name IS NOT (SELECT name FROM projects WHERE projects.id = pieces.project_id);

ALTER TABLE pieces DROP COLUMN name;
