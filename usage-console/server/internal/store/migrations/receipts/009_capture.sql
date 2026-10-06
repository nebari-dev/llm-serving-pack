-- Replay (§7.5.7) needs each request's x-data-region header back, since
-- rules can match on it. Older receipts have none recorded (NULL).
ALTER TABLE receipts ADD COLUMN data_region text;

-- Content Warden keeps for requests on capturing routes (§9.2: per-route
-- opt-in), masked: no detected value is in it. Kept apart from receipts
-- because Warden writes it and ingest writes the receipt, in either order;
-- a receipt finds its content by id. Same 30-day hot window as receipts.
CREATE TABLE receipt_content (
  tenant_id  text NOT NULL,
  receipt_id text NOT NULL,
  ts         timestamptz NOT NULL,
  route      text NOT NULL,
  content    jsonb NOT NULL,
  PRIMARY KEY (receipt_id, ts)
);
SELECT create_hypertable('receipt_content', by_range('ts', INTERVAL '1 day'));
CREATE INDEX receipt_content_tenant_ts ON receipt_content (tenant_id, ts DESC);
SELECT add_retention_policy('receipt_content', INTERVAL '30 days', if_not_exists => true);
