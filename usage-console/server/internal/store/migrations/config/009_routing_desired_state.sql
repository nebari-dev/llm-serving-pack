-- Routes and backends become the gateway's desired state (spec §4.4): the
-- console edits them here and stargate-api compiles them to the gateway's
-- AIGatewayRoute and per-backend resources (internal/routing). The values are
-- what server/aigw/config.yaml had by hand on 2026-10-05, so the first apply
-- changes nothing but Warden's reroute hint for local and openrouter.

-- Where the gateway reaches each backend. Host and port may be aigw
-- ${VAR:-default}s. A backend without them (a seeded one the gateway never
-- had) isn't compiled, and no route may target it.
ALTER TABLE backends
  ADD COLUMN schema      text,
  ADD COLUMN prefix      text,
  ADD COLUMN host        text,
  ADD COLUMN port        text,
  ADD COLUMN tls         boolean NOT NULL DEFAULT false,
  ADD COLUMN api_key_env text,
  ADD CONSTRAINT backends_endpoint CHECK ((schema IS NULL) = (host IS NULL) AND (host IS NULL) = (port IS NULL));

UPDATE backends SET schema = 'OpenAI', prefix = '/' || name || '/v1', host = '${STARGATE_HOST:-localhost}', port = '8090'
WHERE name IN ('openai-prod', 'anthropic-prod', 'bedrock-eu', 'vllm-internal');
UPDATE backends SET schema = 'OpenAI', prefix = '${LOCAL_LLM_PREFIX:-/engines/v1}', host = '${LOCAL_LLM_HOST:-localhost}', port = '${LOCAL_LLM_PORT:-12434}'
WHERE name = 'local';
UPDATE backends SET schema = 'OpenAI', prefix = '/api/v1', host = 'openrouter.ai', port = '443', tls = true, api_key_env = 'OPENROUTER_API_KEY'
WHERE name = 'openrouter';

-- The seeded routes (default, cheap-summarize, eu-private, research-frontier)
-- never reached the gateway. A route is now one AIGatewayRoute rule: match is
-- {models, headers}, targets [{backend, model?, weight?}], fallback the same
-- in the order they're tried. Rules go in ordinal order.
DROP TABLE routes;
CREATE TABLE routes (
  tenant_id       text NOT NULL REFERENCES tenants(id),
  name            text NOT NULL,
  ordinal         integer NOT NULL,
  match           jsonb NOT NULL,
  targets         jsonb NOT NULL,
  fallback        jsonb NOT NULL,
  capture_content boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, name)
);

INSERT INTO routes (tenant_id, name, ordinal, match, targets, fallback)
SELECT 'demo', v.name, v.n, v.match::jsonb, v.targets::jsonb, v.fallback::jsonb
FROM (VALUES
  ('gpt-5', 0, '{"models":["gpt-5-mini","gpt-5.5"],"headers":[]}', '[{"backend":"openai-prod"}]', '[]'),
  ('summarize', 1, '{"models":["summarize-*"],"headers":[]}', '[{"backend":"openai-prod","model":"gpt-5-mini"}]', '[]'),
  ('claude-sonnet-5', 2, '{"models":["claude-sonnet-5"],"headers":[]}', '[{"backend":"anthropic-prod"}]', '[{"backend":"bedrock-eu"}]'),
  ('claude-opus-4-1', 3, '{"models":["claude-opus-4-1"],"headers":[]}', '[{"backend":"anthropic-prod"}]', '[{"backend":"openai-prod","model":"gpt-5.5"}]'),
  ('claude-haiku-4-5', 4, '{"models":["claude-haiku-4-5"],"headers":[]}', '[{"backend":"bedrock-eu"}]', '[]'),
  ('llama-3.3-70b', 5, '{"models":["llama-3.3-70b"],"headers":[]}', '[{"backend":"vllm-internal"}]', '[]'),
  ('smollm2', 6, '{"models":["smollm2"],"headers":[]}', '[{"backend":"local","model":"${LOCAL_LLM_MODEL:-ai/smollm2:360M-Q4_K_M}"}]', '[]'),
  ('gpt-4o-mini', 7, '{"models":["gpt-4o-mini"],"headers":[]}', '[{"backend":"openrouter","model":"openai/gpt-4o-mini"}]', '[]')
) v(name, n, match, targets, fallback)
WHERE EXISTS (SELECT 1 FROM tenants WHERE id = 'demo');

-- Each apply: who, whether the gateway took it, the gateway's error if not,
-- and what it changed ([{kind, name, change}]).
CREATE TABLE routing_applies (
  id        bigserial PRIMARY KEY,
  tenant_id text NOT NULL REFERENCES tenants(id),
  ts        timestamptz NOT NULL DEFAULT now(),
  actor     text NOT NULL,
  ok        boolean NOT NULL,
  error     text,
  changes   jsonb NOT NULL
);
CREATE INDEX routing_applies_tenant_ts ON routing_applies (tenant_id, ts DESC);
