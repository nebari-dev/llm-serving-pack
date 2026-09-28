-- §4.6 hot tier: raw receipts are dropped 30 days after they're written.
-- The continuous aggregates are the cold tier and keep no drop policy. Their
-- refresh windows (3 hours, 3 days) never reach back to dropped chunks; a
-- manual refresh over dropped time would empty those buckets, so don't.
SELECT add_retention_policy('receipts', INTERVAL '30 days', if_not_exists => true);
