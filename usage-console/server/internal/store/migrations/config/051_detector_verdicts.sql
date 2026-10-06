-- A security reviewer's verdict on one detector hit: an entity a receipt
-- records as redacted or blocked, marked a false positive or confirmed.
-- Receipts live in the other database, so a verdict carries the receipt's id
-- and time; it's here, beside audit_log, so the verdict and its audit row
-- commit together (§6). receipt_ts is what false-positive counts window on.
CREATE TABLE detector_verdicts (
  tenant_id  text NOT NULL REFERENCES tenants(id),
  receipt_id text NOT NULL,
  receipt_ts timestamptz NOT NULL,
  entity     text NOT NULL,
  verdict    text NOT NULL CHECK (verdict IN ('false_positive','confirmed')),
  actor      text NOT NULL,
  ts         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, receipt_id, entity)
);
CREATE INDEX detector_verdicts_receipt_ts ON detector_verdicts (tenant_id, receipt_ts DESC);
