-- Real upstreams next to the fake ones: a model on this machine through any
-- OpenAI-compatible server (Docker Model Runner by default) and one through
-- OpenRouter, plus a key allowed both. demo.go seeds the same rows on a fresh
-- database, so here they're added only to a demo tenant that already exists.
INSERT INTO model_catalog
SELECT * FROM (VALUES
  ('smollm2', 'SmolLM2 360M (local)', 'Self-hosted', 'smollm', 8192),
  ('gpt-4o-mini', 'GPT-4o mini (OpenRouter)', 'OpenRouter', 'gpt-4o', 128000)
) v
WHERE EXISTS (SELECT 1 FROM tenants WHERE id = 'demo')
ON CONFLICT DO NOTHING;

INSERT INTO backends
SELECT v.name, 'demo', (SELECT coalesce(max(ordinal), 0) FROM backends) + v.n, v.provider, v.region,
       'console', 'synced', NULL, v.models, 'healthy', 0, 0, false
FROM (VALUES
  ('local', 1, 'Self-hosted', 'local', ARRAY['smollm2']),
  ('openrouter', 2, 'OpenRouter', 'global', ARRAY['gpt-4o-mini'])
) v(name, n, provider, region, models)
WHERE EXISTS (SELECT 1 FROM tenants WHERE id = 'demo')
ON CONFLICT DO NOTHING;

INSERT INTO price_sources
SELECT 'gpt-4o-mini', 'openrouter', 'openrouter/openai/gpt-4o-mini'
WHERE EXISTS (SELECT 1 FROM backends WHERE name = 'openrouter')
ON CONFLICT DO NOTHING;

INSERT INTO api_keys (id, tenant_id, name, prefix, hash, team_id, project, allowed_models, allowed_regions, status)
SELECT 'k8', 'demo', 'local-dev', 'ngw_live_10ca',
       encode(sha256(convert_to('ngw_live_10ca_devsecret_not_for_production', 'UTF8')), 'hex'),
       'research', 'local-models', ARRAY['smollm2', 'gpt-4o-mini'], ARRAY['local', 'global'], 'active'
WHERE EXISTS (SELECT 1 FROM tenants WHERE id = 'demo')
ON CONFLICT DO NOTHING;
