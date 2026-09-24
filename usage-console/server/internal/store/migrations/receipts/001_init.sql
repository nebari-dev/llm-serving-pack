-- Receipts (spec §5.1) as a Timescale hypertable, plus the continuous
-- aggregates every chart reads from (§4.6).

CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE receipts (
  id                  text NOT NULL,
  ts                  timestamptz NOT NULL,
  tenant_id           text NOT NULL,
  trace_id            text NOT NULL,
  session_id          text,
  duration_ms         integer NOT NULL,
  ttft_ms             integer,
  key_id              text NOT NULL,
  key_name            text NOT NULL,
  team                text NOT NULL,
  project             text NOT NULL,
  actor               text,
  requested_model     text NOT NULL,
  resolved_model      text NOT NULL,
  backend             text NOT NULL,
  provider            text NOT NULL,
  region              text NOT NULL,
  route_reason        text NOT NULL,
  fallback_from       text,
  input_tokens        integer NOT NULL,
  cached_input_tokens integer NOT NULL,
  output_tokens       integer NOT NULL,
  reasoning_tokens    integer NOT NULL,
  total_tokens        integer NOT NULL,
  cost_usd            numeric(18,8) NOT NULL,
  cost_basis          jsonb,                 -- price snapshot at request time (§13)
  verdict             text NOT NULL,
  inbound_verdict     text NOT NULL,
  redactions          jsonb NOT NULL DEFAULT '[]',
  rules               jsonb NOT NULL DEFAULT '[]',
  status              integer NOT NULL,
  error_code          text,
  error_detail        text,
  request_hash        text NOT NULL,
  response_hash       text NOT NULL,
  content_captured    boolean NOT NULL DEFAULT false,
  content             jsonb,
  in_flight           boolean NOT NULL DEFAULT false,
  route_trace         jsonb NOT NULL DEFAULT '[]',
  PRIMARY KEY (id, ts)
);

SELECT create_hypertable('receipts', by_range('ts', INTERVAL '1 day'));
CREATE INDEX receipts_tenant_ts ON receipts (tenant_id, ts DESC);
CREATE INDEX receipts_key_ts ON receipts (key_id, ts DESC);

ALTER TABLE receipts SET (
  timescaledb.compress,
  timescaledb.compress_segmentby = 'tenant_id',
  timescaledb.compress_orderby = 'ts DESC'
);
SELECT add_compression_policy('receipts', INTERVAL '7 days');

-- In-flight rows are excluded: tokens and cost arrive last (§13).
CREATE MATERIALIZED VIEW receipts_5m
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       tenant_id, team, key_id, resolved_model, backend, verdict,
       count(*)                 AS requests,
       sum(cost_usd)            AS cost_usd,
       sum(total_tokens)        AS tokens,
       count(*) FILTER (WHERE status >= 500 OR status = 429) AS errors
FROM receipts
WHERE NOT in_flight
GROUP BY bucket, tenant_id, team, key_id, resolved_model, backend, verdict
WITH NO DATA;

SELECT add_continuous_aggregate_policy('receipts_5m',
  start_offset => INTERVAL '3 hours', end_offset => INTERVAL '5 minutes',
  schedule_interval => INTERVAL '1 minute');

CREATE MATERIALIZED VIEW receipts_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket(INTERVAL '1 day', ts) AS bucket,
       tenant_id, team, key_id, resolved_model, verdict,
       count(*)          AS requests,
       sum(cost_usd)     AS cost_usd,
       sum(total_tokens) AS tokens
FROM receipts
WHERE NOT in_flight
GROUP BY bucket, tenant_id, team, key_id, resolved_model, verdict
WITH NO DATA;

SELECT add_continuous_aggregate_policy('receipts_daily',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '1 hour',
  schedule_interval => INTERVAL '15 minutes');
