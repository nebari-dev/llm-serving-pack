-- Prices are per (model, backend), and each rate says where it came from:
-- the demo seed, LiteLLM's price file, or a manual override (decisions §1).
-- A missing rate has no price. Cache writes get their own rate.
CREATE TABLE model_pricing_v2 (
  model_id          text NOT NULL REFERENCES model_catalog(id),
  backend           text NOT NULL,
  in_per_m          numeric(12,6),
  cached_per_m      numeric(12,6),
  cache_write_per_m numeric(12,6),
  out_per_m         numeric(12,6),
  reasoning_per_m   numeric(12,6),
  in_src            text CHECK (in_src IN ('seed','litellm','manual')),
  cached_src        text CHECK (cached_src IN ('seed','litellm','manual')),
  cache_write_src   text CHECK (cache_write_src IN ('seed','litellm','manual')),
  out_src           text CHECK (out_src IN ('seed','litellm','manual')),
  reasoning_src     text CHECK (reasoning_src IN ('seed','litellm','manual')),
  effective_from    timestamptz NOT NULL,
  effective_to      timestamptz,
  PRIMARY KEY (model_id, backend, effective_from),
  CHECK ((in_per_m IS NULL) = (in_src IS NULL) AND (cached_per_m IS NULL) = (cached_src IS NULL)
     AND (cache_write_per_m IS NULL) = (cache_write_src IS NULL) AND (out_per_m IS NULL) = (out_src IS NULL)
     AND (reasoning_per_m IS NULL) = (reasoning_src IS NULL))
);

-- Every backend serving a model starts at the model's rows. The demo seed's
-- rows start 2026-01-01; any later one was set by hand. Cache writes start
-- at the input rate.
INSERT INTO model_pricing_v2
SELECT p.model_id, b.name, p.in_per_m, p.cached_per_m, p.in_per_m, p.out_per_m, p.reasoning_per_m,
       s.src, s.src, s.src, s.src, s.src, p.effective_from, p.effective_to
FROM model_pricing p
JOIN backends b ON p.model_id = ANY (b.models)
CROSS JOIN LATERAL (SELECT CASE WHEN p.effective_from = '2026-01-01' THEN 'seed' ELSE 'manual' END AS src) s;

DROP TABLE model_pricing;
ALTER TABLE model_pricing_v2 RENAME TO model_pricing;
ALTER TABLE model_pricing RENAME CONSTRAINT model_pricing_v2_pkey TO model_pricing_pkey;

-- Which LiteLLM entry prices each pair. No row: the pair has no list price.
CREATE TABLE price_sources (
  model_id    text NOT NULL REFERENCES model_catalog(id),
  backend     text NOT NULL,
  litellm_key text NOT NULL,
  PRIMARY KEY (model_id, backend)
);

INSERT INTO price_sources
SELECT v.m, v.b, v.k
FROM (VALUES
  ('gpt-5-mini', 'openai-prod', 'gpt-5-mini'),
  ('gpt-5.5', 'openai-prod', 'gpt-5.5'),
  ('claude-sonnet-5', 'anthropic-prod', 'claude-sonnet-5'),
  ('claude-haiku-4-5', 'bedrock-eu', 'eu.anthropic.claude-haiku-4-5-20251001-v1:0'),
  ('claude-sonnet-5', 'bedrock-eu', 'eu.anthropic.claude-sonnet-5'),
  ('gpt-5-mini', 'azure-openai-eu', 'azure/eu/gpt-5-mini-2025-08-07')
) v(m, b, k)
JOIN backends b ON b.name = v.b AND v.m = ANY (b.models)
ON CONFLICT DO NOTHING;

-- The last value LiteLLM gave each rate of a pair. A sync that sees it move
-- on an overridden rate proposes the new value instead of applying it.
CREATE TABLE litellm_seen (
  model_id text NOT NULL,
  backend  text NOT NULL,
  rate     text NOT NULL CHECK (rate IN ('input','cachedInput','cacheWrite','output','reasoning')),
  per_m    numeric(12,6) NOT NULL,
  seen_at  timestamptz NOT NULL,
  PRIMARY KEY (model_id, backend, rate)
);

CREATE TABLE price_proposals (
  id             bigserial PRIMARY KEY,
  model_id       text NOT NULL,
  backend        text NOT NULL,
  rate           text NOT NULL CHECK (rate IN ('input','cachedInput','cacheWrite','output','reasoning')),
  current_per_m  numeric(12,6) NOT NULL,
  proposed_per_m numeric(12,6) NOT NULL,
  litellm_key    text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','dismissed')),
  decided_by     text,
  decided_at     timestamptz
);
-- A newer move replaces the open proposal for the same rate.
CREATE UNIQUE INDEX price_proposals_open ON price_proposals (model_id, backend, rate) WHERE status = 'open';

CREATE TABLE price_syncs (
  id          bigserial PRIMARY KEY,
  started_at  timestamptz NOT NULL,
  finished_at timestamptz NOT NULL,
  error       text,
  applied     integer NOT NULL DEFAULT 0,
  proposed    integer NOT NULL DEFAULT 0,
  retired     integer NOT NULL DEFAULT 0
);

-- Sync writes are audited as the sync, not the console.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_source_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_source_check CHECK (source IN ('console','git','sync'));
