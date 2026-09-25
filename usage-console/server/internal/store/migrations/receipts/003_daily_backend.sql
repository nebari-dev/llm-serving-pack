-- Spend by provider at daily grain needs the backend, so receipts_daily
-- groups by it too. A continuous aggregate can't gain a GROUP BY column in
-- place, so it's rebuilt from raw receipts. That is lossless only while raw
-- receipts still cover all history (no retention policy drops them yet); once
-- one does, a change like this needs a new aggregate instead. Store.Migrate
-- refreshes the rebuilt view afterwards, since a refresh can't run inside the
-- transaction this file runs in.
DROP MATERIALIZED VIEW receipts_daily;

CREATE MATERIALIZED VIEW receipts_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket(INTERVAL '1 day', ts) AS bucket,
       tenant_id, team, key_id, resolved_model, backend, verdict,
       count(*)          AS requests,
       sum(cost_usd)     AS cost_usd,
       sum(total_tokens) AS tokens
FROM receipts
WHERE NOT in_flight
GROUP BY bucket, tenant_id, team, key_id, resolved_model, backend, verdict
WITH NO DATA;

SELECT add_continuous_aggregate_policy('receipts_daily',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '1 hour',
  schedule_interval => INTERVAL '15 minutes');
