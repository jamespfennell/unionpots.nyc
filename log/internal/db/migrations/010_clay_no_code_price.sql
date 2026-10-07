-- Clay bodies don't need a product code or price: anything like that goes in
-- the notes. They were never filled in, so they're simply dropped.
ALTER TABLE clays DROP COLUMN code;
ALTER TABLE clays DROP COLUMN price;
