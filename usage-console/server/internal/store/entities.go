package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
)

// DetectorKind is the audit target kind for custom entities and detector
// verdicts.
const DetectorKind = "Detector"

// CustomEntity is a security user's own entity type (§5.3's custom entity
// type registry): a regex the engine runs like a built-in detector, the
// label its placeholder uses ([LABEL_1]), and examples it must and mustn't
// match, checked on every save. gateway.ValidateCustomEntity decides what's
// allowed; the store only keeps it.
type CustomEntity struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Pattern      string   `json:"pattern"`
	Label        string   `json:"label"`
	MustMatch    []string `json:"mustMatch"`
	MustNotMatch []string `json:"mustNotMatch"`
	UpdatedAt    int64    `json:"updatedAt"` // epoch ms
	UpdatedBy    string   `json:"updatedBy"`
	ETag         string   `json:"etag"`
}

// CustomEntityETag is an entity's version for If-Match: everything an edit
// can change (the name can't).
func CustomEntityETag(e CustomEntity) string {
	return ETag([]any{e.ID, e.Name, e.Pattern, e.Label, e.MustMatch, e.MustNotMatch})
}

const entityCols = `id, name, pattern, label, must_match, must_not_match, updated_at, updated_by`

func scanEntity(r pgx.Row) (CustomEntity, error) {
	var e CustomEntity
	var must, mustNot []byte
	var at time.Time
	if err := r.Scan(&e.ID, &e.Name, &e.Pattern, &e.Label, &must, &mustNot, &at, &e.UpdatedBy); err != nil {
		return e, err
	}
	if err := json.Unmarshal(must, &e.MustMatch); err != nil {
		return e, err
	}
	if err := json.Unmarshal(mustNot, &e.MustNotMatch); err != nil {
		return e, err
	}
	e.UpdatedAt = at.UnixMilli()
	e.ETag = CustomEntityETag(e)
	return e, nil
}

// CustomEntities lists the tenant's custom entities by name.
func (s *Store) CustomEntities(ctx context.Context, tenant string) ([]CustomEntity, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+entityCols+` FROM custom_entities WHERE tenant_id = $1 ORDER BY lower(name)`, tenant)
	return collect(rows, func(r pgx.Rows) (CustomEntity, error) { return scanEntity(r) })
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CreateCustomEntity adds e (already validated). A name another entity has,
// ignoring case, is ErrConflict.
func (s *Store) CreateCustomEntity(ctx context.Context, tenant, actor string, e CustomEntity) (CustomEntity, error) {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	e.ID = "ce" + hex.EncodeToString(b)
	e.MustMatch, e.MustNotMatch = nonNil(e.MustMatch), nonNil(e.MustNotMatch)
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	must, _ := json.Marshal(e.MustMatch)
	mustNot, _ := json.Marshal(e.MustNotMatch)
	row := tx.QueryRow(ctx, `INSERT INTO custom_entities (id, tenant_id, name, pattern, label, must_match, must_not_match, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+entityCols, e.ID, tenant, e.Name, e.Pattern, e.Label, must, mustNot, actor)
	out, err := scanEntity(row)
	if err != nil {
		return e, uniqueConflict(err)
	}
	if err := audit(ctx, tx, tenant, actor, "Added custom entity", out.Name, DetectorKind, out.ID, nil, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func customEntity(ctx context.Context, tx pgx.Tx, tenant, id string) (CustomEntity, error) {
	e, err := scanEntity(tx.QueryRow(ctx, `SELECT `+entityCols+` FROM custom_entities WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// UpdateCustomEntity changes an entity's pattern, label and examples (its
// name stays: rules name it). ifMatch is the version the caller saw.
func (s *Store) UpdateCustomEntity(ctx context.Context, tenant, actor, id, ifMatch string, e CustomEntity) (CustomEntity, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	was, err := customEntity(ctx, tx, tenant, id)
	if err != nil {
		return was, err
	}
	if ifMatch != "" && ifMatch != was.ETag {
		return was, &StaleError{Current: was}
	}
	must, _ := json.Marshal(nonNil(e.MustMatch))
	mustNot, _ := json.Marshal(nonNil(e.MustNotMatch))
	now, err := scanEntity(tx.QueryRow(ctx, `UPDATE custom_entities SET pattern = $3, label = $4, must_match = $5, must_not_match = $6, updated_at = now(), updated_by = $7
		WHERE tenant_id = $1 AND id = $2 RETURNING `+entityCols, tenant, id, e.Pattern, e.Label, must, mustNot, actor))
	if err != nil {
		return was, err
	}
	if CustomEntityETag(now) == was.ETag {
		return was, nil // nothing changed: no new row in the audit log
	}
	if err := audit(ctx, tx, tenant, actor, "Changed custom entity", now.Name, DetectorKind, id, was, now); err != nil {
		return was, err
	}
	return now, tx.Commit(ctx)
}

// DeleteCustomEntity removes an entity. The caller checks no rule names it.
func (s *Store) DeleteCustomEntity(ctx context.Context, tenant, actor, id, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	was, err := customEntity(ctx, tx, tenant, id)
	if err != nil {
		return err
	}
	if ifMatch != "" && ifMatch != was.ETag {
		return &StaleError{Current: was}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM custom_entities WHERE tenant_id = $1 AND id = $2`, tenant, id); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted custom entity", was.Name, DetectorKind, id, was, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- detector verdicts -----------------------------------------------------

// HitKey is one detector hit: an entity a receipt recorded.
type HitKey struct{ ReceiptID, Entity string }

// DetectorVerdict is a reviewer's call on one hit: "false_positive" (the
// detector matched something that wasn't the entity) or "confirmed".
type DetectorVerdict struct {
	ReceiptID string `json:"receiptId"`
	ReceiptTS int64  `json:"receiptTs"` // epoch ms
	Entity    string `json:"entity"`
	Verdict   string `json:"verdict"`
	By        string `json:"by"`
	At        int64  `json:"at"` // epoch ms
}

// VerdictETag is a hit's review state for If-Match; nil is "not reviewed".
func VerdictETag(v *DetectorVerdict) string {
	if v == nil {
		return ETag("unreviewed")
	}
	return ETag([]any{v.ReceiptID, v.Entity, v.Verdict, v.By, v.At})
}

// DetectorVerdicts are the verdicts on hits whose receipts are since since.
func (s *Store) DetectorVerdicts(ctx context.Context, tenant string, since time.Time) (map[HitKey]DetectorVerdict, error) {
	rows, err := s.Config.Query(ctx, `SELECT receipt_id, receipt_ts, entity, verdict, actor, ts FROM detector_verdicts WHERE tenant_id = $1 AND receipt_ts >= $2`, tenant, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[HitKey]DetectorVerdict{}
	for rows.Next() {
		var v DetectorVerdict
		var rts, at time.Time
		if err := rows.Scan(&v.ReceiptID, &rts, &v.Entity, &v.Verdict, &v.By, &at); err != nil {
			return nil, err
		}
		v.ReceiptTS, v.At = rts.UnixMilli(), at.UnixMilli()
		out[HitKey{v.ReceiptID, v.Entity}] = v
	}
	return out, rows.Err()
}

// VerdictCounts is one detector's reviewed hits over the last 30 days (by
// the receipt's time, the raw-receipt window).
type VerdictCounts struct{ FalsePositives, Confirmed int }

func (s *Store) VerdictCounts(ctx context.Context, tenant string) (map[string]VerdictCounts, error) {
	rows, err := s.Config.Query(ctx, `SELECT entity, count(*) FILTER (WHERE verdict = 'false_positive')::int, count(*) FILTER (WHERE verdict = 'confirmed')::int
		FROM detector_verdicts WHERE tenant_id = $1 AND receipt_ts > now() - interval '30 days' GROUP BY 1`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]VerdictCounts{}
	for rows.Next() {
		var e string
		var c VerdictCounts
		if err := rows.Scan(&e, &c.FalsePositives, &c.Confirmed); err != nil {
			return nil, err
		}
		out[e] = c
	}
	return out, rows.Err()
}

// SetDetectorVerdict records v as actor's verdict, replacing any earlier
// one, with an audit row in the same transaction. ifMatch is the review
// state the caller saw (VerdictETag); a verdict someone else gave since is
// a StaleError carrying it.
func (s *Store) SetDetectorVerdict(ctx context.Context, tenant, actor string, v DetectorVerdict, ifMatch string) (DetectorVerdict, error) {
	if v.Verdict != "false_positive" && v.Verdict != "confirmed" {
		return v, errors.New("verdict must be false_positive or confirmed")
	}
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	var cur *DetectorVerdict
	var c DetectorVerdict
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT verdict, actor, ts FROM detector_verdicts WHERE tenant_id = $1 AND receipt_id = $2 AND entity = $3 FOR UPDATE`,
		tenant, v.ReceiptID, v.Entity).Scan(&c.Verdict, &c.By, &at)
	switch {
	case err == nil:
		c.ReceiptID, c.ReceiptTS, c.Entity, c.At = v.ReceiptID, v.ReceiptTS, v.Entity, at.UnixMilli()
		cur = &c
	case !errors.Is(err, pgx.ErrNoRows):
		return v, err
	}
	if ifMatch != VerdictETag(cur) {
		return v, &StaleError{Current: cur}
	}
	// A first verdict inserts; if another reviewer's landed meanwhile, the
	// insert does nothing and this caller is told, as for any stale write.
	var ts time.Time
	err = tx.QueryRow(ctx, `INSERT INTO detector_verdicts (tenant_id, receipt_id, receipt_ts, entity, verdict, actor) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, receipt_id, entity) DO UPDATE SET verdict = EXCLUDED.verdict, actor = EXCLUDED.actor, ts = now()
		WHERE $7 RETURNING ts`, tenant, v.ReceiptID, time.UnixMilli(v.ReceiptTS), v.Entity, v.Verdict, actor, cur != nil).Scan(&ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, &StaleError{Current: nil}
	}
	if err != nil {
		return v, err
	}
	v.By, v.At = actor, ts.UnixMilli()
	action := "Marked detector hit a false positive"
	if v.Verdict == "confirmed" {
		action = "Confirmed detector hit"
	}
	var before any
	if cur != nil {
		before = cur
	}
	if err := audit(ctx, tx, tenant, actor, action, fmt.Sprintf("%s on receipt %s", v.Entity, v.ReceiptID), ReviewKind, v.ReceiptID, before, v); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}

// DetectorHitReceipts are the receipts since since that record a detector
// hit: a redaction, or a policy block that names its entity. Newest first.
func (s *Store) DetectorHitReceipts(ctx context.Context, tenant string, since time.Time, limit int) ([]model.Receipt, error) {
	rows, _ := s.Receipts.Query(ctx, selectReceipt+` WHERE tenant_id = $1 AND ts >= $2 AND ts > now() - interval '30 days'
		AND (redactions <> '[]'::jsonb OR error_code = 'policy_blocked' AND error_detail LIKE '%matched entity "%')
		ORDER BY ts DESC LIMIT $3`, tenant, since, limit)
	return collect(rows, func(r pgx.Rows) (model.Receipt, error) { return scanReceipt(r) })
}
