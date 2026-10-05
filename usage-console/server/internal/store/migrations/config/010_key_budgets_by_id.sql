-- A key budget names its key by id, not by name (decided 2026-10-05): a
-- rename or a reused name would move it to another key. Each one points at
-- the key that has its name, preferring one that isn't revoked, then the
-- newest. A name no key has is left as it was and still covers nothing.
-- On a fresh database there are no budgets yet; the seed writes ids.
UPDATE budgets b SET scope = (
  SELECT k.id FROM api_keys k
  WHERE k.tenant_id = b.tenant_id AND k.name = b.scope
  ORDER BY k.status = 'revoked', k.created_at DESC, k.id
  LIMIT 1)
WHERE b.scope_type = 'key'
  AND EXISTS (SELECT 1 FROM api_keys k WHERE k.tenant_id = b.tenant_id AND k.name = b.scope);
