package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pricing writes. Rows are effective-dated and never edited in place: a
// change closes the model's open row and adds one, so receipts keep the rate
// they were costed with (their cost_basis). The catalog is shared by every
// tenant, so a change here reprices everyone's traffic.

const priceTime = "2006-01-02 15:04 UTC"

// ValidatePrice checks a new row. latest is when the model's newest row
// takes effect; the new one must come after it, and not before now.
func ValidatePrice(p PriceRow, latest, now time.Time) error {
	for _, r := range []float64{p.InPerM, p.OutPerM, p.CachedPerM, p.ReasoningPerM} {
		switch {
		case r < 0:
			return errors.New("rates can't be negative")
		case r >= 1e6: // numeric(12,6)
			return errors.New("rates must be under $1,000,000 per million tokens")
		case math.Abs(r*1e6-math.Round(r*1e6)) > 1e-3:
			return errors.New("rates have at most 6 decimal places")
		}
	}
	switch {
	case p.From.Before(now.Add(-time.Minute)):
		return errors.New("a price can't take effect in the past: receipts already costed keep their rate")
	case !p.From.After(latest):
		return fmt.Errorf("a price already takes effect at %s; cancel it first", latest.UTC().Format(priceTime))
	}
	return nil
}

// rate is a per-million price for the audit log: "$2", "$0.30", "$0.025".
func rate(f float64) string {
	s := fmt.Sprintf("%.6f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i == 2 {
		s += "0"
	}
	return "$" + s
}

// priceChange is the audit target for a price change, or "" if no rate moved.
func priceChange(was, now PriceRow) string {
	var parts []string
	for _, f := range []struct {
		name     string
		from, to float64
	}{
		{"input", was.InPerM, now.InPerM}, {"cached input", was.CachedPerM, now.CachedPerM},
		{"output", was.OutPerM, now.OutPerM}, {"reasoning", was.ReasoningPerM, now.ReasoningPerM},
	} {
		if f.from != f.to {
			parts = append(parts, f.name+" "+rate(f.from)+" → "+rate(f.to))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return now.ModelID + " " + strings.Join(parts, ", ") + " per 1M from " + now.From.UTC().Format(priceTime)
}

// ErrSamePrice is a change that moves no rate.
var ErrSamePrice = errors.New("those are the rates already in effect")

const priceCols = `model_id, in_per_m::float8, out_per_m::float8, cached_per_m::float8, reasoning_per_m::float8, effective_from, effective_to`

func scanPrice(r pgx.Row) (PriceRow, error) {
	var p PriceRow
	err := r.Scan(&p.ModelID, &p.InPerM, &p.OutPerM, &p.CachedPerM, &p.ReasoningPerM, &p.From, &p.To)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// SetPrice adds a model's next price row, from p.From, closing the one open now.
func (s *Store) SetPrice(ctx context.Context, tenant, actor string, p PriceRow, now time.Time) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The newest row is the open one; locking it serializes changes to a model.
	last, err := scanPrice(tx.QueryRow(ctx, `SELECT `+priceCols+` FROM model_pricing WHERE model_id = $1 ORDER BY effective_from DESC LIMIT 1 FOR UPDATE`, p.ModelID))
	if err != nil {
		return err
	}
	if err := ValidatePrice(p, last.From, now); err != nil {
		return badPrice{err}
	}
	target := priceChange(last, p)
	if target == "" {
		return badPrice{ErrSamePrice}
	}
	if _, err := tx.Exec(ctx, `UPDATE model_pricing SET effective_to = $2 WHERE model_id = $1 AND effective_from = $3`, p.ModelID, p.From, last.From); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_pricing VALUES ($1,$2,$3,$4,$5,$6,NULL)`, p.ModelID, p.InPerM, p.OutPerM, p.CachedPerM, p.ReasoningPerM, p.From); err != nil {
		return err
	}
	action := "Changed model price"
	if p.From.After(now.Add(time.Minute)) {
		action = "Scheduled model price change"
	}
	if err := audit(ctx, tx, tenant, actor, action, target, "Pricing", p.ModelID, last, p); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CancelPrice removes a scheduled price row that hasn't taken effect, and
// reopens the row before it.
func (s *Store) CancelPrice(ctx context.Context, tenant, actor, modelID string, from, now time.Time) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p, err := scanPrice(tx.QueryRow(ctx, `SELECT `+priceCols+` FROM model_pricing WHERE model_id = $1 AND effective_from = $2 FOR UPDATE`, modelID, from))
	if err != nil {
		return err
	}
	if !p.From.After(now) {
		return badPrice{errors.New("this price is already in effect; set a new one instead")}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_pricing WHERE model_id = $1 AND effective_from = $2`, modelID, from); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE model_pricing SET effective_to = $3 WHERE model_id = $1 AND effective_to = $2`, modelID, from, p.To); err != nil {
		return err
	}
	target := modelID + " change from " + from.UTC().Format(priceTime)
	if err := audit(ctx, tx, tenant, actor, "Cancelled model price change", target, "Pricing", modelID, p, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// badPrice is a price write that fails validation (a 400 at the API).
type badPrice struct{ error }

// IsBadPrice reports whether err is a price write's validation failure.
func IsBadPrice(err error) bool { return errors.As(err, new(badPrice)) }
