-- Policies as spec §5.2 has them: a policy is an ordered list of rules with
-- one mode and one fail mode (§4.5), versioned, published and rolled back as
-- a unit. Mode is §5.2's status: 'draft' (never published), 'enforce' or
-- 'monitor' (active, §7.5.7's monitor mode), or 'disabled'. Until now each
-- rule was versioned on its own.
--
-- Every rule becomes a policy of its own: the policy takes the rule's id,
-- name, description, mode, fail mode, version and place in the order, and
-- holds the rule under the same id and name. That's the grouping that
-- changes nothing: rules had their own modes and fail modes, which a shared
-- policy would have to merge, and each rule's version history becomes its
-- policy's, version for version. Receipts and audit rows that name a rule's
-- id name its policy's. gateway.TestMigratedPoliciesDecideAsTheRulesDid
-- checks the engine decides as before over rules migrated this way.

CREATE TABLE policies (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES tenants(id),
  ordinal     integer NOT NULL,
  name        text NOT NULL,
  description text NOT NULL,
  mode        text NOT NULL CHECK (mode IN ('draft','enforce','monitor','disabled')),
  fail_mode   text NOT NULL CHECK (fail_mode IN ('open','closed')),
  version     integer NOT NULL
);
CREATE UNIQUE INDEX policies_name ON policies (tenant_id, name);

INSERT INTO policies (id, tenant_id, ordinal, name, description, mode, fail_mode, version)
  SELECT id, tenant_id, ordinal, name, description, mode, fail_mode, version FROM policy_rules;

-- A published version holds the policy's rules, in order, as
-- [{id, name, when, then}].
CREATE TABLE policy_versions (
  tenant_id    text NOT NULL REFERENCES tenants(id),
  policy_id    text NOT NULL, -- no foreign key: history outlives a deleted policy
  version      integer NOT NULL,
  name         text NOT NULL,
  description  text NOT NULL,
  mode         text NOT NULL,
  fail_mode    text NOT NULL,
  rules        jsonb NOT NULL,
  published_at timestamptz, -- null: published before versions were kept
  published_by text,
  PRIMARY KEY (policy_id, version)
);
-- Deleted rules' history comes too, under their ids.
INSERT INTO policy_versions (tenant_id, policy_id, version, name, description, mode, fail_mode, rules, published_at, published_by)
  SELECT tenant_id, rule_id, version, name, description, mode, fail_mode,
         jsonb_build_array(jsonb_build_object('id', rule_id, 'name', name, 'when', "when", 'then', "then")),
         published_at, published_by
  FROM policy_rule_versions;
-- A database seeded after migration 004 has no version rows for its seeded
-- rules (the seed didn't write them). Their live version is known, so it's
-- recorded, as 004 did for the rules it found.
INSERT INTO policy_versions (tenant_id, policy_id, version, name, description, mode, fail_mode, rules)
  SELECT r.tenant_id, r.id, r.version, r.name, r.description, r.mode, r.fail_mode,
         jsonb_build_array(jsonb_build_object('id', r.id, 'name', r.name, 'when', r."when", 'then', r."then"))
  FROM policy_rules r
  WHERE r.version > 0 AND NOT EXISTS (SELECT 1 FROM policy_rule_versions v WHERE v.rule_id = r.id AND v.version = r.version);

CREATE TABLE policy_drafts (
  policy_id   text PRIMARY KEY REFERENCES policies(id) ON DELETE CASCADE,
  name        text NOT NULL,
  description text NOT NULL,
  fail_mode   text NOT NULL,
  rules       jsonb NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  text NOT NULL
);
INSERT INTO policy_drafts (policy_id, name, description, fail_mode, rules, updated_at, updated_by)
  SELECT rule_id, name, description, fail_mode,
         jsonb_build_array(jsonb_build_object('id', rule_id, 'name', name, 'when', "when", 'then', "then")),
         updated_at, updated_by
  FROM policy_rule_drafts;
-- A never-published rule always had a draft; if one didn't, its row was its
-- content, so that becomes the draft.
INSERT INTO policy_drafts (policy_id, name, description, fail_mode, rules, updated_by)
  SELECT id, name, description, fail_mode,
         jsonb_build_array(jsonb_build_object('id', id, 'name', name, 'when', "when", 'then', "then")),
         'migration 045'
  FROM policy_rules r
  WHERE version = 0 AND NOT EXISTS (SELECT 1 FROM policy_rule_drafts d WHERE d.rule_id = r.id);

DROP TABLE policy_rule_drafts;
DROP TABLE policy_rule_versions;

-- policy_rules now holds each policy's live rules, in order. A
-- never-published policy has none: its content is its draft.
DELETE FROM policy_rules WHERE version = 0;
DROP INDEX policy_rules_name;
ALTER TABLE policy_rules DROP CONSTRAINT policy_rules_pkey;
ALTER TABLE policy_rules ADD COLUMN policy_id text;
UPDATE policy_rules SET policy_id = id, ordinal = 1;
ALTER TABLE policy_rules
  DROP COLUMN tenant_id,
  DROP COLUMN description,
  DROP COLUMN mode,
  DROP COLUMN fail_mode,
  DROP COLUMN version,
  ALTER COLUMN policy_id SET NOT NULL,
  ADD CONSTRAINT policy_rules_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES policies(id) ON DELETE CASCADE,
  ADD PRIMARY KEY (policy_id, id);
CREATE UNIQUE INDEX policy_rules_name ON policy_rules (policy_id, name);
