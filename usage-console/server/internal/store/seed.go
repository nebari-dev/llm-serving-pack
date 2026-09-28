package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/demo"
)

// Seed loads the demo tenant into the config db. It is a no-op if the tenant
// already exists, so it is safe to run on every start.
func (s *Store) Seed(ctx context.Context) (bool, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'Demo tenant') ON CONFLICT DO NOTHING`, demo.Tenant)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	t := demo.Tenant
	now := time.Now()

	b := &pgx.Batch{}
	for _, x := range demo.Teams {
		b.Queue(`INSERT INTO teams VALUES ($1,$2,$3,$4)`, x.ID, t, x.Name, x.CostCenter)
	}
	for _, m := range demo.Models {
		b.Queue(`INSERT INTO model_catalog VALUES ($1,$2,$3,$4,$5)`, m.ID, m.Display, m.Provider, m.Family, m.Context)
		b.Queue(`INSERT INTO model_pricing VALUES ($1,$2,$3,$4,$5,'2026-01-01',NULL)`, m.ID, m.InPerM, m.OutPerM, m.CachedPerM, m.ReasoningPerM)
	}
	for alias, target := range demo.Aliases {
		b.Queue(`INSERT INTO model_aliases VALUES ($1,$2)`, alias, target)
	}
	for i, x := range demo.Backends {
		b.Queue(`INSERT INTO backends VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12,$13)`,
			x.Name, t, i, x.Provider, x.Region, x.Provenance, x.Sync, x.Source, x.Models, x.Health, x.P50, x.ErrorRate, x.CaptureContent)
	}
	for i, x := range demo.Routes {
		targets, _ := json.Marshal(x.Targets)
		b.Queue(`INSERT INTO routes VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			x.Name, t, i, x.Match, targets, x.Fallback, x.Provenance, x.Sync, x.CaptureContent)
	}
	for _, x := range demo.Budgets {
		b.Queue(`INSERT INTO budgets VALUES ($1,$2,$3,$4,$5,$6,$7)`, x.ID, t, x.ScopeType, x.Scope, x.Period, x.CapUSD, x.OnExceed)
	}
	for _, k := range demo.Keys {
		var revokedAt *time.Time
		if k.Status == "revoked" {
			revokedAt = &now
		}
		b.Queue(`INSERT INTO api_keys (id, tenant_id, name, prefix, hash, team_id, project, allowed_models, allowed_regions, budget_id, expires_at, status, revoked_at)
		         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12,$13)`,
			k.ID, t, k.Name, k.Prefix, demo.HashSecret(demo.DevSecret(k.Prefix)), k.Team, k.Project, k.AllowedModels, k.AllowedRegions, k.BudgetID, k.ExpiresAt, k.Status, revokedAt)
	}
	for _, r := range demo.Rules {
		when, _ := json.Marshal(r.When)
		then, _ := json.Marshal(r.Then)
		b.Queue(`INSERT INTO policy_rules VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, r.ID, t, r.Ordinal, r.Name, r.Description, r.Mode, r.FailMode, r.Version, when, then)
	}
	for _, d := range demo.Detectors {
		b.Queue(`INSERT INTO detectors VALUES ($1,$2,$3,$4,$5,$6,$7)`, d.ID, t, d.Name, d.Kind, d.Threshold, d.Hits24h, d.FP)
	}
	keyIDs := map[string]string{}
	for _, k := range demo.Keys {
		keyIDs[k.Name] = k.ID
	}
	for _, c := range demo.Changes(now) {
		var targetID *string
		if id, ok := keyIDs[c.Target]; ok && c.TargetKind == "Key" {
			targetID = &id
		}
		b.Queue(`INSERT INTO audit_log (tenant_id, ts, actor, action, target, target_kind, target_id, effect, effect_tone, source)
		         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			t, time.UnixMilli(c.TS), c.Actor, c.Action, c.Target, c.TargetKind, targetID, c.Effect, c.EffectTone, c.Source)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
