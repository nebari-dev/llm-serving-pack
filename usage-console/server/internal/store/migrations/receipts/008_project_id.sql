-- The project the request's key belongs to, by id (§5.1), so Traffic tells
-- two teams' same-named projects apart and a rename doesn't split history.
-- Null for requests no key identified. Store.Migrate backfills older rows
-- from each key's project after this runs: a key never changes project, so
-- that's exact. The continuous aggregates don't need it: they keep key_id,
-- and Spend groups keys by their project.
ALTER TABLE receipts ADD COLUMN project_id text;
