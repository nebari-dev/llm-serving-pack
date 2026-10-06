-- §5.3's custom entity type registry: a security user's own entity types,
-- each a regex with the placeholder a redaction writes, and examples it must
-- and mustn't match. Warden reads them with the rest of its snapshot; rules
-- name them in "contains entity" like the built-ins. The API validates a
-- pattern (RE2, bounded size, never empty) before it lands here. Names are
-- how rules refer to an entity, so one can't change, and no two match
-- ignoring case.
CREATE TABLE custom_entities (
  id             text PRIMARY KEY,
  tenant_id      text NOT NULL REFERENCES tenants(id),
  name           text NOT NULL,
  pattern        text NOT NULL,
  label          text NOT NULL,
  must_match     jsonb NOT NULL DEFAULT '[]',
  must_not_match jsonb NOT NULL DEFAULT '[]',
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     text NOT NULL
);
CREATE UNIQUE INDEX custom_entities_name ON custom_entities (tenant_id, lower(name));
