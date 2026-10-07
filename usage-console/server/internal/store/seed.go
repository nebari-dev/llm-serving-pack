package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

// Seed modes. SeedDemo is the simulated demo: backends served by the fake
// upstream, made-up keys, budgets, policies and history, which the test
// stack and its suite rely on. SeedReal is a clean start with nothing
// simulated: the local model server, its route and models, and the team
// names keys go in.
const (
	SeedReal = "real"
	SeedDemo = "demo"
)

// seedSet is what a seed writes.
type seedSet struct {
	TenantName  string
	Teams       []model.Team
	Models      []model.Model
	Aliases     map[string]string
	Backends    []model.Backend
	Prices      []demo.SeedPrice
	LiteLLMKeys map[[2]string]string
	Routes      []model.Route
	Budgets     []model.Budget
	Projects    []demo.Project
	Keys        []model.APIKey
	Policies    []model.Policy
	Detectors   []model.Detector
	Changes     bool // the seeded audit rows (demo.Changes)
}

func seedFor(mode string) (seedSet, error) {
	switch mode {
	case SeedDemo:
		return seedSet{TenantName: "Demo tenant", Teams: demo.Teams, Models: demo.Models, Aliases: demo.Aliases, Backends: demo.Backends,
			Prices: demo.SeedPrices(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), LiteLLMKeys: demo.LiteLLMKeys, Routes: demo.Routes, Budgets: demo.Budgets,
			Projects: demo.Projects, Keys: demo.Keys, Policies: demo.Policies, Detectors: demo.Detectors, Changes: true}, nil
	case SeedReal:
		s := seedSet{TenantName: "Local", Teams: demo.Teams}
		for _, b := range demo.Backends {
			if b.Name == "local" {
				s.Backends = append(s.Backends, b)
			}
		}
		for _, r := range demo.Routes {
			if len(r.Targets) == 1 && r.Targets[0].Backend == "local" && len(r.Fallback) == 0 {
				s.Routes = append(s.Routes, r)
			}
		}
		for _, m := range demo.Models {
			if m.ID == "smollm2" {
				s.Models = append(s.Models, m)
			}
		}
		return s, nil
	}
	return seedSet{}, fmt.Errorf("unknown seed %q: %s or %s", mode, SeedReal, SeedDemo)
}

// Seed loads the demo tenant into the config db. It is a no-op if the tenant
// already exists, so it is safe to run on every start.
func (s *Store) Seed(ctx context.Context, mode string) (bool, error) {
	set, err := seedFor(mode)
	if err != nil {
		return false, err
	}
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`, demo.Tenant, set.TenantName)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	t := demo.Tenant
	now := time.Now()

	b := &pgx.Batch{}
	for _, x := range set.Teams {
		b.Queue(`INSERT INTO teams VALUES ($1,$2,$3,$4)`, x.ID, t, x.Name, x.CostCenter)
	}
	for _, m := range set.Models {
		b.Queue(`INSERT INTO model_catalog VALUES ($1,$2,$3,$4,$5)`, m.ID, m.Display, m.Provider, m.Family, m.Context)
	}
	for alias, target := range set.Aliases {
		b.Queue(`INSERT INTO model_aliases (tenant_id, alias, target) VALUES ($1,$2,$3)`, t, alias, target)
	}
	for i, x := range set.Backends {
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
	for _, p := range set.Prices {
		b.Queue(`INSERT INTO model_pricing VALUES ($1,$2,$3,$4,$5,$6,$7,'seed','seed','seed','seed','seed',$8,NULL)`,
			p.ModelID, p.Backend, p.Rates[0], p.Rates[1], p.Rates[2], p.Rates[3], p.Rates[4], p.From)
	}
	for pair, key := range set.LiteLLMKeys {
		b.Queue(`INSERT INTO price_sources VALUES ($1,$2,$3)`, pair[0], pair[1], key)
	}
	for i, x := range set.Routes {
		normalizeRoute(&x)
		m, tg, f := marshalRoute(x)
		b.Queue(`INSERT INTO routes (tenant_id, name, ordinal, match, targets, fallback, capture_content) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			t, x.Name, i, m, tg, f, x.CaptureContent)
	}
	for _, x := range set.Budgets {
		b.Queue(`INSERT INTO budgets VALUES ($1,$2,$3,$4,$5,$6,$7)`, x.ID, t, x.ScopeType, x.Scope, x.Period, x.CapUSD, x.OnExceed)
	}
	for _, p := range set.Projects {
		b.Queue(`INSERT INTO projects (id, tenant_id, team_id, name) VALUES ($1,$2,$3,$4)`, p.ID, t, p.Team, p.Name)
	}
	for _, k := range set.Keys {
		var revokedAt *time.Time
		if k.Status == "revoked" {
			revokedAt = &now
		}
		// Seeded keys belong to the dev user, like keys made before sign-in.
		b.Queue(`INSERT INTO api_keys (id, tenant_id, name, prefix, hash, team_id, project_id, allowed_models, allowed_regions, expires_at, status, revoked_at, owner)
		         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'dev@localhost')`,
			k.ID, t, k.Name, k.Prefix, demo.HashSecret(demo.DevSecret(k.Prefix)), k.Team, k.ProjectID, k.AllowedModels, k.AllowedRegions, k.ExpiresAt, k.Status, revokedAt)
	}
	// Each seeded policy's current version is all its history, as migration
	// 045 leaves a database whose rules predate versions.
	for _, p := range set.Policies {
		b.Queue(`INSERT INTO policies (id, tenant_id, ordinal, name, description, mode, fail_mode, version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			p.ID, t, p.Ordinal, p.Name, p.Description, p.Mode, p.FailMode, p.Version)
		for i, r := range p.Rules {
			when, _ := json.Marshal(r.When)
			then, _ := json.Marshal(r.Then)
			b.Queue(`INSERT INTO policy_rules (policy_id, id, ordinal, name, "when", "then") VALUES ($1,$2,$3,$4,$5,$6)`, p.ID, r.ID, i+1, r.Name, when, then)
		}
		rules, _ := json.Marshal(p.Rules)
		b.Queue(`INSERT INTO policy_versions (tenant_id, policy_id, version, name, description, mode, fail_mode, rules) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			t, p.ID, p.Version, p.Name, p.Description, p.Mode, p.FailMode, rules)
	}
	for _, d := range set.Detectors {
		b.Queue(`INSERT INTO detectors VALUES ($1,$2,$3,$4,$5,$6,$7)`, d.ID, t, d.Name, d.Kind, d.Threshold, d.Hits24h, d.FP)
	}
	keyIDs := map[string]string{}
	for _, k := range set.Keys {
		keyIDs[k.Name] = k.ID
	}
	for _, c := range changes(set, now) {
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

func changes(set seedSet, now time.Time) []model.Change {
	if !set.Changes {
		return nil
	}
	return demo.Changes(now)
}
