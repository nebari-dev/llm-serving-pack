package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

func (s *Store) Teams(ctx context.Context, tenant string) ([]model.Team, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, name, cost_center FROM teams WHERE tenant_id = $1 ORDER BY id = 'support' DESC, name`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Team, error) {
		var t model.Team
		return t, r.Scan(&t.ID, &t.Name, &t.CostCenter)
	})
}

// Models returns the catalog with the price in effect now.
func (s *Store) Models(ctx context.Context) ([]model.Model, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT c.id, c.display, c.provider, c.family, c.context,
		       p.in_per_m::float8, p.out_per_m::float8, p.cached_per_m::float8, p.reasoning_per_m::float8
		FROM model_catalog c
		JOIN LATERAL (
		  SELECT * FROM model_pricing p WHERE p.model_id = c.id AND p.effective_from <= now()
		    AND (p.effective_to IS NULL OR p.effective_to > now())
		  ORDER BY effective_from DESC LIMIT 1) p ON true
		ORDER BY c.provider = 'OpenAI' DESC, c.provider = 'Anthropic' DESC, c.provider, p.in_per_m`)
	return collect(rows, func(r pgx.Rows) (model.Model, error) {
		var m model.Model
		return m, r.Scan(&m.ID, &m.Display, &m.Provider, &m.Family, &m.Context, &m.InPerM, &m.OutPerM, &m.CachedPerM, &m.ReasoningPerM)
	})
}

func (s *Store) Aliases(ctx context.Context) (map[string]string, error) {
	rows, _ := s.Config.Query(ctx, `SELECT alias, target FROM model_aliases`)
	pairs, err := collect(rows, func(r pgx.Rows) ([2]string, error) {
		var p [2]string
		return p, r.Scan(&p[0], &p[1])
	})
	out := map[string]string{}
	for _, p := range pairs {
		out[p[0]] = p[1]
	}
	return out, err
}

func (s *Store) Backends(ctx context.Context, tenant string) ([]model.Backend, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT name, provider, region, provenance, sync_state, coalesce(source_ref, ''), models, health, p50_ms, error_rate::float8, capture_content
		FROM backends WHERE tenant_id = $1 ORDER BY ordinal`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Backend, error) {
		var b model.Backend
		return b, r.Scan(&b.Name, &b.Provider, &b.Region, &b.Provenance, &b.Sync, &b.Source, &b.Models, &b.Health, &b.P50, &b.ErrorRate, &b.CaptureContent)
	})
}

func (s *Store) Routes(ctx context.Context, tenant string) ([]model.Route, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT name, match, targets, fallback, provenance, sync_state, capture_content
		FROM routes WHERE tenant_id = $1 ORDER BY ordinal`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Route, error) {
		var x model.Route
		var targets []byte
		if err := r.Scan(&x.Name, &x.Match, &targets, &x.Fallback, &x.Provenance, &x.Sync, &x.CaptureContent); err != nil {
			return x, err
		}
		return x, json.Unmarshal(targets, &x.Targets)
	})
}

// Budgets returns caps only; spend is filled in from the receipts db.
func (s *Store) Budgets(ctx context.Context, tenant string) ([]model.Budget, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, scope, scope_type, period, cap_usd::float8, on_exceed FROM budgets WHERE tenant_id = $1 ORDER BY id`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Budget, error) {
		var b model.Budget
		return b, r.Scan(&b.ID, &b.Scope, &b.ScopeType, &b.Period, &b.CapUSD, &b.OnExceed)
	})
}

// KeyRecord is an API key plus the credential material the gateway needs.
type KeyRecord struct {
	model.APIKey
	Hash        string
	NextHash    string
	RotateUntil *time.Time
}

const keyCols = `id, name, prefix, team_id, project, allowed_models, allowed_regions, coalesce(budget_id, ''),
	to_char(expires_at, 'YYYY-MM-DD'), status, hash, coalesce(next_hash, ''), rotate_until`

func scanKey(r pgx.Row) (KeyRecord, error) {
	var k KeyRecord
	err := r.Scan(&k.ID, &k.Name, &k.Prefix, &k.Team, &k.Project, &k.AllowedModels, &k.AllowedRegions, &k.BudgetID,
		&k.ExpiresAt, &k.Status, &k.Hash, &k.NextHash, &k.RotateUntil)
	return k, err
}

func (s *Store) Keys(ctx context.Context, tenant string) ([]KeyRecord, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+keyCols+` FROM api_keys WHERE tenant_id = $1 ORDER BY status = 'revoked', created_at, id`, tenant)
	return collect(rows, func(r pgx.Rows) (KeyRecord, error) { return scanKey(r) })
}

func (s *Store) Rules(ctx context.Context, tenant string) ([]model.PolicyRule, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, ordinal, name, description, mode, fail_mode, version, "when", "then" FROM policy_rules WHERE tenant_id = $1 ORDER BY ordinal`, tenant)
	return collect(rows, func(r pgx.Rows) (model.PolicyRule, error) {
		var p model.PolicyRule
		var when, then []byte
		if err := r.Scan(&p.ID, &p.Ordinal, &p.Name, &p.Description, &p.Mode, &p.FailMode, &p.Version, &when, &then); err != nil {
			return p, err
		}
		if err := json.Unmarshal(when, &p.When); err != nil {
			return p, err
		}
		return p, json.Unmarshal(then, &p.Then)
	})
}

func (s *Store) Detectors(ctx context.Context, tenant string) ([]model.Detector, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, name, kind, threshold::float8, hits_24h, fp FROM detectors WHERE tenant_id = $1 ORDER BY ctid`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Detector, error) {
		var d model.Detector
		return d, r.Scan(&d.ID, &d.Name, &d.Kind, &d.Threshold, &d.Hits24h, &d.FP)
	})
}

func (s *Store) Changes(ctx context.Context, tenant string, limit int) ([]model.Change, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT id, ts, actor, action, target, target_kind, coalesce(effect, ''), coalesce(effect_tone, ''), source
		FROM audit_log WHERE tenant_id = $1 ORDER BY ts DESC LIMIT $2`, tenant, limit)
	return collect(rows, func(r pgx.Rows) (model.Change, error) {
		var c model.Change
		var id int64
		var ts time.Time
		err := r.Scan(&id, &ts, &c.Actor, &c.Action, &c.Target, &c.TargetKind, &c.Effect, &c.EffectTone, &c.Source)
		c.ID, c.TS = fmt.Sprintf("c%d", id), ts.UnixMilli()
		return c, err
	})
}

// ---- key mutations ------------------------------------------------------

type NewKey struct {
	Name           string   `json:"name"`
	Team           string   `json:"team"`
	Project        string   `json:"project"`
	AllowedModels  []string `json:"allowedModels"`
	AllowedRegions []string `json:"allowedRegions"`
	BudgetID       string   `json:"budgetId"`
	ExpiresAt      *string  `json:"expiresAt"`
}

func newSecret() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 40)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "ngw_live_" + string(b)
}

func audit(ctx context.Context, tx pgx.Tx, tenant, actor, action, target, kind, id string, before, after any) error {
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, action, target, target_kind, target_id, before, after, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'console')`, tenant, actor, action, target, kind, id, bj, aj)
	return err
}

// CreateKey stores the hash of a freshly generated secret and returns the
// secret once; it is not recoverable afterwards.
func (s *Store) CreateKey(ctx context.Context, tenant, actor string, in NewKey) (KeyRecord, string, error) {
	secret := newSecret()
	id := "k" + demo.HashSecret(secret)[:6]
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return KeyRecord{}, "", err
	}
	defer tx.Rollback(ctx)
	k, err := scanKey(tx.QueryRow(ctx, `
		INSERT INTO api_keys (id, tenant_id, name, prefix, hash, team_id, project, allowed_models, allowed_regions, budget_id, expires_at, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11::date,'active')
		RETURNING `+keyCols,
		id, tenant, in.Name, secret[:13], demo.HashSecret(secret), in.Team, in.Project, in.AllowedModels, in.AllowedRegions, in.BudgetID, in.ExpiresAt))
	if err != nil {
		return KeyRecord{}, "", err
	}
	if err := audit(ctx, tx, tenant, actor, "Created key", k.Name, "Key", k.ID, nil, k.APIKey); err != nil {
		return KeyRecord{}, "", err
	}
	return k, secret, tx.Commit(ctx)
}

func (s *Store) RevokeKey(ctx context.Context, tenant, actor, id string) (KeyRecord, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return KeyRecord{}, err
	}
	defer tx.Rollback(ctx)
	k, err := scanKey(tx.QueryRow(ctx, `
		UPDATE api_keys SET status = 'revoked', revoked_at = now(), next_hash = NULL, rotate_until = NULL
		WHERE tenant_id = $1 AND id = $2 AND status <> 'revoked' RETURNING `+keyCols, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyRecord{}, ErrNotFound
	}
	if err != nil {
		return KeyRecord{}, err
	}
	if err := audit(ctx, tx, tenant, actor, "Revoked key", k.Name, "Key", k.ID, nil, nil); err != nil {
		return KeyRecord{}, err
	}
	return k, tx.Commit(ctx)
}

// FinishRotations promotes the new secret on keys whose overlap window has
// ended, which retires the old one.
func (s *Store) FinishRotations(ctx context.Context) error {
	_, err := s.Config.Exec(ctx, `
		UPDATE api_keys SET hash = next_hash, next_hash = NULL, rotate_until = NULL, status = 'active'
		WHERE status = 'rotating' AND next_hash IS NOT NULL AND rotate_until < now()`)
	return err
}

// RotateKey issues a new secret. Both secrets authenticate until the overlap
// window ends and FinishRotations retires the old one.
func (s *Store) RotateKey(ctx context.Context, tenant, actor, id string, overlap time.Duration) (KeyRecord, string, error) {
	secret := newSecret()
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return KeyRecord{}, "", err
	}
	defer tx.Rollback(ctx)
	k, err := scanKey(tx.QueryRow(ctx, `
		UPDATE api_keys SET status = 'rotating', next_hash = $3, rotate_until = now() + $4::interval
		WHERE tenant_id = $1 AND id = $2 AND status = 'active' RETURNING `+keyCols,
		tenant, id, demo.HashSecret(secret), fmt.Sprintf("%d seconds", int(overlap.Seconds()))))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyRecord{}, "", ErrConflict
	}
	if err != nil {
		return KeyRecord{}, "", err
	}
	if err := audit(ctx, tx, tenant, actor, "Rotated key", k.Name, "Key", k.ID, nil, nil); err != nil {
		return KeyRecord{}, "", err
	}
	return k, secret, tx.Commit(ctx)
}

// TenantName is the tenant's display name.
func (s *Store) TenantName(ctx context.Context, tenant string) (string, error) {
	var name string
	err := s.Config.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, tenant).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
