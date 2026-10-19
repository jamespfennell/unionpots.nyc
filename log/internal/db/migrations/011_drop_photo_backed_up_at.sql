-- Photo backups no longer use a per-photo flag: each backup target is listed
-- to find the photos it's missing, which works for any number of targets.
ALTER TABLE photos DROP COLUMN backed_up_at;
