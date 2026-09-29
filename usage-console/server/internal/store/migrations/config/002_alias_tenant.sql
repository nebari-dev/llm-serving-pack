-- Aliases are per tenant (spec §5.2), now that tenants can write them. Each
-- existing alias is copied to every tenant, since all of them saw it before.
ALTER TABLE model_aliases DROP CONSTRAINT model_aliases_pkey;
ALTER TABLE model_aliases ADD COLUMN tenant_id text REFERENCES tenants(id);
INSERT INTO model_aliases (tenant_id, alias, target)
  SELECT t.id, a.alias, a.target FROM tenants t CROSS JOIN model_aliases a WHERE a.tenant_id IS NULL;
DELETE FROM model_aliases WHERE tenant_id IS NULL;
ALTER TABLE model_aliases ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE model_aliases ADD PRIMARY KEY (tenant_id, alias);
