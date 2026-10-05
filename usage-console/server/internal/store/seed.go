package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
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
	}
	for alias, target := range demo.Aliases {
		b.Queue(`INSERT INTO model_aliases (tenant_id, alias, target) VALUES ($1,$2,$3)`, t, alias, target)
	}
	for i, x := range demo.Backends {
		var e model.BackendEndpoint
		if x.Endpoint != nil {
			e = *x.Endpoint
		}
		b.Queue(`INSERT INTO backends (name, tenant_id, ordinal, provider, region, provenance, sync_state, source_ref, models, health, p50_ms, error_rate, capture_content,
		                               schema, prefix, host, port, tls, api_key_env)
		         VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12,$13,NULLIF($14,''),NULLIF($15,''),NULLIF($16,''),NULLIF($17,''),$18,NULLIF($19,''))`,
			x.Name, t, i, x.Provider, x.Region, x.Provenance, x.Sync, x.Source, x.Models, x.Health, x.P50, x.ErrorRate, x.CaptureContent,
			e.Schema, e.Prefix, e.Host, e.Port, e.TLS, e.APIKeyEnv)
	}
	for _, p := range demo.SeedPrices(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		b.Queue(`INSERT INTO model_pricing VALUES ($1,$2,$3,$4,$5,$6,$7,'seed','seed','seed','seed','seed',$8,NULL)`,
			p.ModelID, p.Backend, p.Rates[0], p.Rates[1], p.Rates[2], p.Rates[3], p.Rates[4], p.From)
	}
	for pair, key := range demo.LiteLLMKeys {
		b.Queue(`INSERT INTO price_sources VALUES ($1,$2,$3)`, pair[0], pair[1], key)
	}
	for i, x := range demo.Routes {
		normalizeRoute(&x)
		m, tg, f := marshalRoute(x)
		b.Queue(`INSERT INTO routes (tenant_id, name, ordinal, match, targets, fallback, capture_content) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			t, x.Name, i, m, tg, f, x.CaptureContent)
	}
	for _, x := range demo.Budgets {
		b.Queue(`INSERT INTO budgets VALUES ($1,$2,$3,$4,$5,$6,$7)`, x.ID, t, x.ScopeType, x.Scope, x.Period, x.CapUSD, x.OnExceed)
	}
	for _, p := range demo.Projects {
		b.Queue(`INSERT INTO projects (id, tenant_id, team_id, name) VALUES ($1,$2,$3,$4)`, p.ID, t, p.Team, p.Name)
	}
	for _, k := range demo.Keys {
		var revokedAt *time.Time
		if k.Status == "revoked" {
			revokedAt = &now
		}
		b.Queue(`INSERT INTO api_keys (id, tenant_id, name, prefix, hash, team_id, project_id, allowed_models, allowed_regions, expires_at, status, revoked_at)
		         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			k.ID, t, k.Name, k.Prefix, demo.HashSecret(demo.DevSecret(k.Prefix)), k.Team, k.ProjectID, k.AllowedModels, k.AllowedRegions, k.ExpiresAt, k.Status, revokedAt)
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
