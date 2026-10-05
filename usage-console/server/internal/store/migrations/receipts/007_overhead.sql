-- The gateway's own time on a request (spec G6: at most 10ms p50): from the
-- whole request received to the first byte sent upstream, in microseconds.
-- Null before this was recorded, and when nothing went upstream.
ALTER TABLE receipts ADD COLUMN overhead_us integer;
