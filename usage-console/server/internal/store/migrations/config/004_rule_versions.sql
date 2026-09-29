-- Published rule versions are immutable (spec §7.5.7). policy_rules holds the
-- live version Warden enforces; an edit waits in policy_rule_drafts until it
-- is published as the next version. A rule can also be disabled.
ALTER TABLE policy_rules DROP CONSTRAINT policy_rules_mode_check;
ALTER TABLE policy_rules ADD CONSTRAINT policy_rules_mode_check CHECK (mode IN ('enforce','monitor','draft','disabled'));
CREATE UNIQUE INDEX policy_rules_name ON policy_rules (tenant_id, name);

CREATE TABLE policy_rule_versions (
  tenant_id    text NOT NULL REFERENCES tenants(id),
  rule_id      text NOT NULL, -- no foreign key: history outlives a deleted rule
  version      integer NOT NULL,
  name         text NOT NULL,
  description  text NOT NULL,
  mode         text NOT NULL,
  fail_mode    text NOT NULL,
  "when"       jsonb NOT NULL,
  "then"       jsonb NOT NULL,
  published_at timestamptz, -- null: published before versions were kept
  published_by text,
  PRIMARY KEY (rule_id, version)
);
-- Only each rule's current version is known; earlier ones weren't kept.
INSERT INTO policy_rule_versions (tenant_id, rule_id, version, name, description, mode, fail_mode, "when", "then")
  SELECT tenant_id, id, version, name, description, mode, fail_mode, "when", "then" FROM policy_rules WHERE mode <> 'draft' AND version > 0;

CREATE TABLE policy_rule_drafts (
  rule_id     text PRIMARY KEY REFERENCES policy_rules(id) ON DELETE CASCADE,
  name        text NOT NULL,
  description text NOT NULL,
  fail_mode   text NOT NULL,
  "when"      jsonb NOT NULL,
  "then"      jsonb NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  text NOT NULL
);
