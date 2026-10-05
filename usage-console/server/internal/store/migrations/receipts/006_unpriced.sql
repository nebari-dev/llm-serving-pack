-- A request whose (model, backend) has no price has no cost, rather than $0
-- (decisions §1); it's priced once someone sets a rate for the pair. Cache
-- writes are counted so they can bill at their own rate.
ALTER TABLE receipts ALTER COLUMN cost_usd DROP NOT NULL;
ALTER TABLE receipts ADD COLUMN cache_write_tokens integer NOT NULL DEFAULT 0;
CREATE INDEX receipts_unpriced ON receipts (resolved_model, backend) WHERE cost_usd IS NULL;
