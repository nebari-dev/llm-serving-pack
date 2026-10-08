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

// Models returns the catalog. Prices are per (model, backend): PriceRows.
func (s *Store) Models(ctx context.Context) ([]model.Model, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT id, display, provider, family, context FROM model_catalog
		ORDER BY provider = 'OpenAI' DESC, provider = 'Anthropic' DESC, provider, context, id`)
	return collect(rows, func(r pgx.Rows) (model.Model, error) {
		var m model.Model
		return m, r.Scan(&m.ID, &m.Display, &m.Provider, &m.Family, &m.Context)
	})
}

func (s *Store) Aliases(ctx context.Context, tenant string) (map[string]string, error) {
	rows, _ := s.Config.Query(ctx, `SELECT alias, target FROM model_aliases WHERE tenant_id = $1`, tenant)
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

// Budgets returns caps only; spend is filled in from the receipts db.
func (s *Store) Budgets(ctx context.Context, tenant string) ([]model.Budget, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+budgetCols+` FROM budgets WHERE tenant_id = $1 ORDER BY id`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Budget, error) { return scanBudget(r) })
}

// KeyRecord is an API key plus the credential material the gateway needs.
type KeyRecord struct {
	model.APIKey
	Hash        string
	NextHash    string
	RotateUntil *time.Time
}

// keyCols reads a key with its project's name, which receipts carry. A
// subquery rather than a join, so RETURNING can use it too.
const keyCols = `id, name, prefix, team_id, (SELECT p.name FROM projects p WHERE p.id = api_keys.project_id), project_id,
	allowed_models, allowed_regions, to_char(expires_at, 'YYYY-MM-DD'), status, hash, coalesce(next_hash, ''), rotate_until, owner`

func scanKey(r pgx.Row) (KeyRecord, error) {
	var k KeyRecord
	err := r.Scan(&k.ID, &k.Name, &k.Prefix, &k.Team, &k.Project, &k.ProjectID, &k.AllowedModels, &k.AllowedRegions,
		&k.ExpiresAt, &k.Status, &k.Hash, &k.NextHash, &k.RotateUntil, &k.Owner)
	return k, err
}

func (s *Store) Keys(ctx context.Context, tenant string) ([]KeyRecord, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+keyCols+` FROM api_keys WHERE tenant_id = $1 ORDER BY status = 'revoked', created_at, id`, tenant)
	return collect(rows, func(r pgx.Rows) (KeyRecord, error) { return scanKey(r) })
}

// AccessKind is the target kind of audit rows that record someone reading
// receipts (an export, a content reveal), not changing config.
const AccessKind = "Receipt"

// ExportKind is a report exported (the close report); ReviewKind is a
// reviewer's verdict on a detector hit. Neither changes config.
const (
	ExportKind = "Export"
	ReviewKind = "Review"
)

// notChanges are the audit kinds that record reading or judging, not
// changing config.
var notChanges = []string{AccessKind, ExportKind, ReviewKind}

// Changes is the newest audit rows, of only kind, or with no kind every
// config change. Rows of the notChanges kinds are left out then: they
// changed nothing, so Activity, Overview and the receipt drawer mustn't
// present them as changes with a traffic effect. Ask for their kind to list
// them.
func (s *Store) Changes(ctx context.Context, tenant string, limit int, kind string) ([]model.Change, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT id, ts, actor, action, target, target_kind, coalesce(effect, ''), coalesce(effect_tone, ''), source
		FROM audit_log WHERE tenant_id = $1 AND ($3 = '' AND NOT target_kind = ANY($4) OR target_kind = $3) ORDER BY ts DESC, id DESC LIMIT $2`, tenant, limit, kind, notChanges)
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
	Name string `json:"name"`
	Team string `json:"team"`
	// ProjectID is one of Team's projects. The API also takes Project, the
	// project's name within the team, and resolves it (KeyProject).
	ProjectID      string   `json:"projectId"`
	Project        string   `json:"project"`
	AllowedModels  []string `json:"allowedModels"`
	AllowedRegions []string `json:"allowedRegions"`
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
	return auditFrom(ctx, tx, tenant, actor, action, target, kind, id, before, after, "console")
}

// auditFrom is audit for a change made outside the console (source "sync").
func auditFrom(ctx context.Context, tx pgx.Tx, tenant, actor, action, target, kind, id string, before, after any, source string) error {
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, action, target, target_kind, target_id, before, after, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenant, actor, action, target, kind, id, bj, aj, source)
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
	// The project must still be the team's, and not deleted; FOR SHARE
	// holds off a delete until the key is in.
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE tenant_id = $1 AND id = $2 AND team_id = $3 AND deleted_at IS NULL FOR SHARE)`,
		tenant, in.ProjectID, in.Team).Scan(&live); err != nil {
		return KeyRecord{}, "", err
	}
	if !live {
		return KeyRecord{}, "", ErrNotFound
	}
	k, err := scanKey(tx.QueryRow(ctx, `
		INSERT INTO api_keys (id, tenant_id, name, prefix, hash, team_id, project_id, allowed_models, allowed_regions, expires_at, status, owner)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,'active',$11)
		RETURNING `+keyCols,
		id, tenant, in.Name, secret[:13], demo.HashSecret(secret), in.Team, in.ProjectID, in.AllowedModels, in.AllowedRegions, in.ExpiresAt, actor))
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

// AuditMark is when an audit action happened, and who did it.
type AuditMark struct {
	TS    time.Time
	Actor string
}

// RotationStarts is each key's latest "Rotated key" audit row, by key id.
func (s *Store) RotationStarts(ctx context.Context, tenant string) (map[string]AuditMark, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT DISTINCT ON (target_id) target_id, ts, actor FROM audit_log
		WHERE tenant_id = $1 AND target_kind = 'Key' AND action = 'Rotated key' AND target_id IS NOT NULL
		ORDER BY target_id, ts DESC`, tenant)
	type mark struct {
		id string
		AuditMark
	}
	ms, err := collect(rows, func(r pgx.Rows) (mark, error) {
		var m mark
		return m, r.Scan(&m.id, &m.TS, &m.Actor)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]AuditMark, len(ms))
	for _, m := range ms {
		out[m.id] = m.AuditMark
	}
	return out, nil
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

// RotationSecrets counts each rotating key's requests since its rotation
// started, by the secret that authenticated them ("" when not recorded).
// Raw receipts: the aggregates don't keep the secret, and the windows are
// at most a week.
func (s *Store) RotationSecrets(ctx context.Context, tenant string, since map[string]time.Time) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	for id, from := range since {
		rows, _ := s.Receipts.Query(ctx, `
			SELECT coalesce(secret_id, ''), count(*)::int FROM receipts
			WHERE tenant_id = $1 AND key_id = $2 AND ts >= $3 AND NOT in_flight GROUP BY 1`, tenant, id, from)
		type row struct {
			secret string
			n      int
		}
		got, err := collect(rows, func(r pgx.Rows) (row, error) {
			var x row
			return x, r.Scan(&x.secret, &x.n)
		})
		if err != nil {
			return nil, err
		}
		out[id] = map[string]int{}
		for _, x := range got {
			out[id][x.secret] = x.n
		}
	}
	return out, nil
}

// MaxOverlap is the longest a rotation's overlap may run from now.
const MaxOverlap = 7 * 24 * time.Hour

// ErrOverlapTooLong is an extension past MaxOverlap from now.
var ErrOverlapTooLong = errors.New("the overlap can't end more than 7 days from now")

// ExtendRotation pushes a rotating key's overlap end back by d.
func (s *Store) ExtendRotation(ctx context.Context, tenant, actor, id string, d time.Duration) (KeyRecord, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return KeyRecord{}, err
	}
	defer tx.Rollback(ctx)
	var until *time.Time
	err = tx.QueryRow(ctx, `SELECT rotate_until FROM api_keys WHERE tenant_id = $1 AND id = $2 AND status = 'rotating' AND next_hash IS NOT NULL FOR UPDATE`, tenant, id).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyRecord{}, ErrConflict
	}
	if err != nil {
		return KeyRecord{}, err
	}
	end := time.Now().Add(d)
	if until != nil && until.After(time.Now()) {
		end = until.Add(d)
	}
	if time.Until(end) > MaxOverlap {
		return KeyRecord{}, ErrOverlapTooLong
	}
	k, err := scanKey(tx.QueryRow(ctx, `UPDATE api_keys SET rotate_until = $3 WHERE tenant_id = $1 AND id = $2 RETURNING `+keyCols, tenant, id, end))
	if err != nil {
		return KeyRecord{}, err
	}
	target := k.Name + " · until " + end.UTC().Format("2006-01-02 15:04") + " UTC"
	if err := audit(ctx, tx, tenant, actor, "Extended rotation overlap", target, "Key", k.ID, map[string]any{"rotateUntil": until}, map[string]any{"rotateUntil": end}); err != nil {
		return KeyRecord{}, err
	}
	return k, tx.Commit(ctx)
}

// FinishRotation retires a rotating key's old secret now, as FinishRotations
// does when the overlap ends.
func (s *Store) FinishRotation(ctx context.Context, tenant, actor, id string) (KeyRecord, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return KeyRecord{}, err
	}
	defer tx.Rollback(ctx)
	k, err := scanKey(tx.QueryRow(ctx, `
		UPDATE api_keys SET hash = next_hash, next_hash = NULL, rotate_until = NULL, status = 'active'
		WHERE tenant_id = $1 AND id = $2 AND status = 'rotating' AND next_hash IS NOT NULL RETURNING `+keyCols, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyRecord{}, ErrConflict
	}
	if err != nil {
		return KeyRecord{}, err
	}
	if err := audit(ctx, tx, tenant, actor, "Retired old secret", k.Name, "Key", k.ID, nil, nil); err != nil {
		return KeyRecord{}, err
	}
	return k, tx.Commit(ctx)
}

// LastChange is who last changed something, and when (epoch ms).
type LastChange struct {
	Actor string
	At    int64
}

// LastChanges is the latest audit row per target id of a kind ("Alias"):
// who last created or changed each, and when.
func (s *Store) LastChanges(ctx context.Context, tenant, kind string) (map[string]LastChange, error) {
	rows, err := s.Config.Query(ctx, `SELECT DISTINCT ON (target_id) target_id, actor, ts FROM audit_log
		WHERE tenant_id = $1 AND target_kind = $2 AND target_id IS NOT NULL AND target_id <> ''
		ORDER BY target_id, ts DESC`, tenant, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LastChange{}
	for rows.Next() {
		var id, actor string
		var ts time.Time
		if err := rows.Scan(&id, &actor, &ts); err != nil {
			return nil, err
		}
		out[id] = LastChange{Actor: actor, At: ts.UnixMilli()}
	}
	return out, rows.Err()
}
