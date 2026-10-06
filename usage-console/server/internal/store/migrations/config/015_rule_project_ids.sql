-- A rule's "project is" condition names projects by id (§5.1), so a rename
-- doesn't change what it matches and two teams' same-named projects are
-- told apart. A name becomes the id of every project with that name, which
-- is what it matched before; a value that is neither stays, and matches
-- nothing, as before. Drafts and version history are converted too, so a
-- rollback restores a rule the engine reads the same way.
CREATE FUNCTION pg_temp.project_ids(tenant text, conds jsonb) RETURNS jsonb LANGUAGE sql AS $$
  SELECT coalesce(jsonb_agg(CASE WHEN e.c->>'field' = 'project' THEN jsonb_set(e.c, '{value}', (
      SELECT coalesce(jsonb_agg(DISTINCT ids.x), '[]'::jsonb) FROM (
        SELECT coalesce(p.id, v.name) AS x
        FROM jsonb_array_elements_text(e.c->'value') AS v(name)
        LEFT JOIN projects p ON p.tenant_id = tenant AND (p.id = v.name OR p.name = v.name)
      ) ids))
    ELSE e.c END ORDER BY e.i), '[]'::jsonb)
  FROM jsonb_array_elements(conds) WITH ORDINALITY AS e(c, i)
$$;

UPDATE policy_rules SET "when" = pg_temp.project_ids(tenant_id, "when")
WHERE "when" @> '[{"field": "project"}]';

UPDATE policy_rule_drafts d SET "when" = pg_temp.project_ids(r.tenant_id, d."when")
FROM policy_rules r
WHERE r.id = d.rule_id AND d."when" @> '[{"field": "project"}]';

UPDATE policy_rule_versions SET "when" = pg_temp.project_ids(tenant_id, "when")
WHERE "when" @> '[{"field": "project"}]';
