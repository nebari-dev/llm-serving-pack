-- One budget per scope: with every covering budget enforced, two on the same
-- team would leave its cap ambiguous.
CREATE UNIQUE INDEX budgets_scope ON budgets (tenant_id, scope_type, scope);
