-- Projects can be renamed and deleted (decided 2026-10-05). Receipts, budgets
-- and rules name a project by id, so its name is only for people: any text,
-- unique within the team ignoring case. A deleted project stays, so the
-- history of its revoked keys keeps a name, and its name can be used again.
ALTER TABLE projects ADD COLUMN deleted_at timestamptz;
ALTER TABLE projects DROP CONSTRAINT projects_tenant_id_team_id_name_key;
CREATE UNIQUE INDEX projects_live_name ON projects (tenant_id, team_id, lower(name)) WHERE deleted_at IS NULL;
