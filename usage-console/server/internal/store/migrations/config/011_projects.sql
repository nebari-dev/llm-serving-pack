-- Projects become a table (§5.2, decided 2026-10-05), so a project budget
-- can be set up before any key in it exists. Names are unique within a team.
CREATE TABLE projects (
  id         text PRIMARY KEY,
  tenant_id  text NOT NULL REFERENCES tenants(id),
  team_id    text NOT NULL REFERENCES teams(id),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, team_id, name),
  UNIQUE (id, team_id) -- for api_keys' (project_id, team_id) reference
);

-- Each team's free-text project names on its keys become its projects, with
-- the ids demo.ProjectID derives, so a database seeded after this migration
-- (store.Seed) and one migrated from the old seed agree.
INSERT INTO projects (id, tenant_id, team_id, name, created_at)
SELECT 'p' || left(encode(sha256(convert_to(tenant_id || '/' || team_id || '/' || project, 'UTF8')), 'hex'), 8),
       tenant_id, team_id, project, min(created_at)
FROM api_keys
GROUP BY tenant_id, team_id, project;

-- A key belongs to one of its own team's projects.
ALTER TABLE api_keys ADD COLUMN project_id text;
UPDATE api_keys k SET project_id = p.id
FROM projects p
WHERE p.tenant_id = k.tenant_id AND p.team_id = k.team_id AND p.name = k.project;
ALTER TABLE api_keys ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE api_keys ADD FOREIGN KEY (project_id, team_id) REFERENCES projects (id, team_id);

-- A project budget names its project by id too. A name several teams used
-- covered all of their keys; it now points at the project of the oldest key
-- with that name. A name no key has is left as it was and covers nothing.
UPDATE budgets b SET scope = (
  SELECT k.project_id FROM api_keys k
  WHERE k.tenant_id = b.tenant_id AND k.project = b.scope
  ORDER BY k.created_at, k.id
  LIMIT 1)
WHERE b.scope_type = 'project'
  AND EXISTS (SELECT 1 FROM api_keys k WHERE k.tenant_id = b.tenant_id AND k.project = b.scope);

-- The name now lives on the project; keys read it from there.
ALTER TABLE api_keys DROP COLUMN project;
