-- A step's date can be approximate: the notebook import fills in dates the
-- notebook doesn't have (7 days per step). The app shows them as "~Mar 13".
-- Nothing in the app sets it; new steps always have a real date.
ALTER TABLE events ADD COLUMN approximate INTEGER NOT NULL DEFAULT 0;
