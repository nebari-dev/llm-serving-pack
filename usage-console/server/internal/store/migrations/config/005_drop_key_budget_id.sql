-- Budgets apply by scope (a key's team, project or name), so a key no longer
-- names one.
ALTER TABLE api_keys DROP COLUMN budget_id;
