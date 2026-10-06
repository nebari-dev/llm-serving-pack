-- Members and key owners (backend-decisions §7, auth and roles).
--
-- users is a cache of who has signed in and the roles their last token's
-- Keycloak groups gave them (spec §5.2's users.role, as a list: a token can
-- carry several groups). Keycloak stays the source; Settings → Members reads
-- this.
CREATE TABLE users (
  tenant_id     text NOT NULL REFERENCES tenants(id),
  email         text NOT NULL,
  name          text NOT NULL DEFAULT '',
  roles         text[] NOT NULL,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, email)
);

-- A key's owner may revoke, rotate, extend and finish it; anyone else needs
-- admin. Existing keys get the actor of their "Created key" audit row, and
-- keys with none (seeded, or made before audit rows) dev@localhost: every
-- write before sign-in was made as dev@localhost, so that is who made them.
-- Once people sign in, nobody is dev@localhost, so those keys are managed by
-- admins until someone recreates them under their own name.
ALTER TABLE api_keys ADD COLUMN owner text;
UPDATE api_keys k SET owner = coalesce((
  SELECT a.actor FROM audit_log a
  WHERE a.tenant_id = k.tenant_id AND a.target_kind = 'Key' AND a.action = 'Created key' AND a.target_id = k.id
  ORDER BY a.ts LIMIT 1), 'dev@localhost');
ALTER TABLE api_keys ALTER COLUMN owner SET NOT NULL;
