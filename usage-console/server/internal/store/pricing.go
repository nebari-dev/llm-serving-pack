package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
)

// Pricing writes. Prices belong to a (model, backend). Rows are
// effective-dated and never edited in place: a change closes the pair's open
// row and adds one, so receipts keep the rate they were costed with (their
// cost_basis). The catalog is shared by every tenant, so a change here
// reprices everyone's traffic on that backend.

const priceTime = "2006-01-02 15:04 UTC"

// SyncActor is who the audit log says made a sync's changes.
const SyncActor = "LiteLLM sync"

// ValidatePrice checks a new row. latest is when the pair's newest row
// takes effect (zero if it has none); the new one must come after it, and
// not before now.
func ValidatePrice(p PriceRow, latest, now time.Time) error {
	for _, r := range p.Rates {
		switch {
		case r == nil:
		case *r < 0:
			return errors.New("rates can't be negative")
		case *r >= 1e6: // numeric(12,6)
			return errors.New("rates must be under $1,000,000 per million tokens")
		case math.Abs(*r*1e6-math.Round(*r*1e6)) > 1e-3:
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

// rate is a per-million price for the audit log: "$2", "$0.30", "$0.025",
// or "no price".
func rate(f *float64) string {
	if f == nil {
		return "no price"
	}
	s := fmt.Sprintf("%.6f", *f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i == 2 {
		s += "0"
	}
	return "$" + s
}

func sameRate(a, b *float64) bool { return pricing.Same(a, b) }

// priceChange is the audit target for a price change, or "" if no rate or
// source moved. A rate that only changed source says so.
func priceChange(was, now PriceRow) string {
	var parts []string
	for i := range pricing.NumRates {
		name := strings.ToLower(pricing.Labels[i])
		switch {
		case !sameRate(was.Rates[i], now.Rates[i]):
			parts = append(parts, name+" "+rate(was.Rates[i])+" → "+rate(now.Rates[i]))
		case was.Sources[i] != now.Sources[i] && now.Sources[i] == pricing.Manual:
			parts = append(parts, name+" "+rate(now.Rates[i])+" now overridden")
		case was.Sources[i] != now.Sources[i] && now.Sources[i] == pricing.LiteLLM:
			parts = append(parts, name+" "+rate(now.Rates[i])+" now follows LiteLLM")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return now.ModelID + " on " + now.Backend + " " + strings.Join(parts, ", ") + " per 1M from " + now.From.UTC().Format(priceTime)
}

// ErrSamePrice is a change that moves no rate.
var ErrSamePrice = errors.New("those are the rates already in effect")

const priceCols = `model_id, backend,
	in_per_m::float8, cached_per_m::float8, cache_write_per_m::float8, out_per_m::float8, reasoning_per_m::float8,
	coalesce(in_src, ''), coalesce(cached_src, ''), coalesce(cache_write_src, ''), coalesce(out_src, ''), coalesce(reasoning_src, ''),
	effective_from, effective_to`

func scanPrice(r pgx.Row) (PriceRow, error) {
	var p PriceRow
	var src [pricing.NumRates]string
	err := r.Scan(&p.ModelID, &p.Backend, &p.Rates[0], &p.Rates[1], &p.Rates[2], &p.Rates[3], &p.Rates[4],
		&src[0], &src[1], &src[2], &src[3], &src[4], &p.From, &p.To)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	for i, s := range src {
		p.Sources[i] = pricing.Source(s)
	}
	return p, err
}

func nullSrc(s pricing.Source) *string {
	if s == "" {
		return nil
	}
	v := string(s)
	return &v
}

func insertPrice(ctx context.Context, tx pgx.Tx, p PriceRow) error {
	_, err := tx.Exec(ctx, `INSERT INTO model_pricing VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		p.ModelID, p.Backend, p.Rates[0], p.Rates[1], p.Rates[2], p.Rates[3], p.Rates[4],
		nullSrc(p.Sources[0]), nullSrc(p.Sources[1]), nullSrc(p.Sources[2]), nullSrc(p.Sources[3]), nullSrc(p.Sources[4]), p.From, p.To)
	return err
}

// newestPrice locks and returns a pair's newest row; ok is false if it has none.
func newestPrice(ctx context.Context, tx pgx.Tx, modelID, backend string) (PriceRow, bool, error) {
	p, err := scanPrice(tx.QueryRow(ctx, `SELECT `+priceCols+` FROM model_pricing WHERE model_id = $1 AND backend = $2
		ORDER BY effective_from DESC LIMIT 1 FOR UPDATE`, modelID, backend))
	if errors.Is(err, ErrNotFound) {
		return PriceRow{ModelID: modelID, Backend: backend}, false, nil
	}
	return p, err == nil, err
}

// PriceVersion is what a pair's etag covers: its newest row, or the pair
// alone when it has never had a price.
func PriceVersion(newest PriceRow) any {
	if newest.From.IsZero() {
		return [2]string{newest.ModelID, newest.Backend}
	}
	return newest
}

func seenRates(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, modelID, backend string) (pricing.Seen, error) {
	var seen pricing.Seen
	rows, _ := q.Query(ctx, `SELECT rate, per_m::float8 FROM litellm_seen WHERE model_id = $1 AND backend = $2`, modelID, backend)
	type rv struct {
		r string
		v float64
	}
	got, err := collect(rows, func(r pgx.Rows) (rv, error) {
		var x rv
		return x, r.Scan(&x.r, &x.v)
	})
	for _, x := range got {
		for i, n := range pricing.Names {
			if n == x.r {
				v := x.v
				seen[i] = &v
			}
		}
	}
	return seen, err
}

// LiteLLMSeen returns the last value LiteLLM gave each rate of each pair.
func (s *Store) LiteLLMSeen(ctx context.Context) (map[[2]string]pricing.Seen, error) {
	rows, _ := s.Config.Query(ctx, `SELECT model_id, backend, rate, per_m::float8 FROM litellm_seen`)
	out := map[[2]string]pricing.Seen{}
	_, err := collect(rows, func(r pgx.Rows) (struct{}, error) {
		var m, b, rt string
		var v float64
		if err := r.Scan(&m, &b, &rt, &v); err != nil {
			return struct{}{}, err
		}
		if i := rateByName(rt); i >= 0 {
			seen := out[[2]string{m, b}]
			seen[i] = &v
			out[[2]string{m, b}] = seen
		}
		return struct{}{}, nil
	})
	return out, err
}

// PriceEdit is a manual change to a pair: each rate in Set becomes an
// override at its value, or, when the value is nil, goes back to following
// LiteLLM at the last value it gave. Rates not in Set keep what they have.
type PriceEdit struct {
	ModelID, Backend string
	Set              map[pricing.Rate]*float64
	From             time.Time
	IfMatch          string
}

// SetPrice adds a pair's next price row, from e.From, closing the open one.
// Open proposals on the rates it touches are dismissed: the edit decides them.
func (s *Store) SetPrice(ctx context.Context, tenant, actor string, e PriceEdit, now time.Time) (PriceRow, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return PriceRow{}, err
	}
	defer tx.Rollback(ctx)
	// The newest row is the open one; locking it serializes changes to a pair.
	last, had, err := newestPrice(ctx, tx, e.ModelID, e.Backend)
	if err != nil {
		return PriceRow{}, err
	}
	if err := checkMatch(e.IfMatch, PriceVersion(last)); err != nil {
		return PriceRow{}, err
	}
	seen, err := seenRates(ctx, tx, e.ModelID, e.Backend)
	if err != nil {
		return PriceRow{}, err
	}
	p := PriceRow{ModelID: e.ModelID, Backend: e.Backend, Rates: last.Rates, Sources: last.Sources, From: e.From}
	if had && last.To != nil {
		// The newest row ended with nothing after it (a retired price).
		p.Rates, p.Sources = pricing.Rates{}, pricing.Sources{}
	}
	var touched []string
	for r, v := range e.Set {
		touched = append(touched, pricing.Names[r])
		if v != nil {
			p.Rates[r], p.Sources[r] = v, pricing.Manual
			continue
		}
		if seen[r] == nil {
			return PriceRow{}, badPrice{fmt.Errorf("LiteLLM has no %s rate for %s on %s to follow; set one", strings.ToLower(pricing.Labels[r]), e.ModelID, e.Backend)}
		}
		p.Rates[r], p.Sources[r] = seen[r], pricing.LiteLLM
	}
	var latest time.Time
	if had {
		latest = last.From
	}
	if err := ValidatePrice(p, latest, now); err != nil {
		return PriceRow{}, badPrice{err}
	}
	target := priceChange(last, p)
	if target == "" {
		return PriceRow{}, badPrice{ErrSamePrice}
	}
	if had && last.To == nil {
		if _, err := tx.Exec(ctx, `UPDATE model_pricing SET effective_to = $3 WHERE model_id = $1 AND backend = $2 AND effective_from = $4`,
			e.ModelID, e.Backend, p.From, last.From); err != nil {
			return PriceRow{}, err
		}
	}
	if err := insertPrice(ctx, tx, p); err != nil {
		return PriceRow{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE price_proposals SET status = 'dismissed', decided_by = $4, decided_at = $5
		WHERE model_id = $1 AND backend = $2 AND rate = ANY($3) AND status = 'open'`, e.ModelID, e.Backend, touched, actor, now); err != nil {
		return PriceRow{}, err
	}
	action := "Changed model price"
	if p.From.After(now.Add(time.Minute)) {
		action = "Scheduled model price change"
	}
	var before any
	if had {
		before = last
	}
	if err := audit(ctx, tx, tenant, actor, action, target, "Pricing", e.ModelID+"@"+e.Backend, before, p); err != nil {
		return PriceRow{}, err
	}
	return p, tx.Commit(ctx)
}

// CancelPrice removes a scheduled price row that hasn't taken effect, and
// reopens the row before it.
func (s *Store) CancelPrice(ctx context.Context, tenant, actor, modelID, backend string, from, now time.Time) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p, err := scanPrice(tx.QueryRow(ctx, `SELECT `+priceCols+` FROM model_pricing WHERE model_id = $1 AND backend = $2 AND effective_from = $3 FOR UPDATE`, modelID, backend, from))
	if err != nil {
		return err
	}
	if !p.From.After(now) {
		return badPrice{errors.New("this price is already in effect; set a new one instead")}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_pricing WHERE model_id = $1 AND backend = $2 AND effective_from = $3`, modelID, backend, from); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE model_pricing SET effective_to = $4 WHERE model_id = $1 AND backend = $2 AND effective_to = $3`, modelID, backend, from, p.To); err != nil {
		return err
	}
	target := modelID + " on " + backend + " change from " + from.UTC().Format(priceTime)
	if err := audit(ctx, tx, tenant, actor, "Cancelled model price change", target, "Pricing", modelID+"@"+backend, p, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PriceSources maps each (model, backend) with a LiteLLM entry to its key.
func (s *Store) PriceSources(ctx context.Context) (map[[2]string]string, error) {
	rows, _ := s.Config.Query(ctx, `SELECT model_id, backend, litellm_key FROM price_sources`)
	got, err := collect(rows, func(r pgx.Rows) ([3]string, error) {
		var x [3]string
		return x, r.Scan(&x[0], &x[1], &x[2])
	})
	out := map[[2]string]string{}
	for _, x := range got {
		out[[2]string{x[0], x[1]}] = x[2]
	}
	return out, err
}

// SetPriceSource points a pair at a LiteLLM key, or at none when key is "".
// The caller checks the key is in the file; the next sync applies it.
func (s *Store) SetPriceSource(ctx context.Context, tenant, actor, modelID, backend, key string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var was string
	if err := tx.QueryRow(ctx, `SELECT litellm_key FROM price_sources WHERE model_id = $1 AND backend = $2 FOR UPDATE`, modelID, backend).Scan(&was); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if was == key {
		return badPrice{errors.New("that's the LiteLLM entry it already uses")}
	}
	if key == "" {
		_, err = tx.Exec(ctx, `DELETE FROM price_sources WHERE model_id = $1 AND backend = $2`, modelID, backend)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO price_sources VALUES ($1,$2,$3) ON CONFLICT (model_id, backend) DO UPDATE SET litellm_key = EXCLUDED.litellm_key`, modelID, backend, key)
	}
	if err != nil {
		return err
	}
	// What LiteLLM said under the old key says nothing about the new one.
	if _, err := tx.Exec(ctx, `DELETE FROM litellm_seen WHERE model_id = $1 AND backend = $2`, modelID, backend); err != nil {
		return err
	}
	name := func(k string) string {
		if k == "" {
			return "none"
		}
		return k
	}
	target := modelID + " on " + backend + " LiteLLM entry " + name(was) + " → " + name(key)
	if err := audit(ctx, tx, tenant, actor, "Changed price source", target, "Pricing", modelID+"@"+backend, map[string]string{"litellmKey": was}, map[string]string{"litellmKey": key}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SyncResult is what one LiteLLM sync did.
type SyncResult struct {
	Applied, Proposed, Retired int
}

// pricedPair is a (model, backend) a tenant's backend serves.
type pricedPair struct{ tenant, model, backend string }

// ApplySync brings every pair a backend serves in line with LiteLLM's file
// (pricing.PlanSync), in one transaction, auditing each change as the sync.
// A change starts now; if a manual change is scheduled, the synced row runs
// until it.
func (s *Store) ApplySync(ctx context.Context, lite map[string]pricing.Rates, started, now time.Time) (SyncResult, error) {
	var res SyncResult
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	rows, _ := tx.Query(ctx, `SELECT DISTINCT tenant_id, unnest(models), name FROM backends ORDER BY 2, 3`)
	pairs, err := collect(rows, func(r pgx.Rows) (pricedPair, error) {
		var p pricedPair
		return p, r.Scan(&p.tenant, &p.model, &p.backend)
	})
	if err != nil {
		return res, err
	}
	keys := map[[2]string]string{}
	rows, _ = tx.Query(ctx, `SELECT model_id, backend, litellm_key FROM price_sources`)
	if _, err := collect(rows, func(r pgx.Rows) (struct{}, error) {
		var m, b, k string
		err := r.Scan(&m, &b, &k)
		keys[[2]string{m, b}] = k
		return struct{}{}, err
	}); err != nil {
		return res, err
	}
	for _, pp := range pairs {
		cur, err := scanPrice(tx.QueryRow(ctx, `SELECT `+priceCols+` FROM model_pricing WHERE model_id = $1 AND backend = $2
			AND effective_from <= $3 AND (effective_to IS NULL OR effective_to > $3) FOR UPDATE`, pp.model, pp.backend, now))
		had := err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return res, err
		}
		if !had {
			cur = PriceRow{ModelID: pp.model, Backend: pp.backend}
		}
		seen, err := seenRates(ctx, tx, pp.model, pp.backend)
		if err != nil {
			return res, err
		}
		key := keys[[2]string{pp.model, pp.backend}]
		var entry *pricing.Rates
		if r, ok := lite[key]; ok && key != "" {
			entry = &r
		}
		plan := pricing.PlanSync(pricing.Current{Rates: cur.Rates, Sources: cur.Sources}, entry, seen)
		for i, v := range plan.Seen {
			if v == nil || sameRate(v, seen[i]) {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO litellm_seen VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (model_id, backend, rate) DO UPDATE SET per_m = EXCLUDED.per_m, seen_at = EXCLUDED.seen_at`,
				pp.model, pp.backend, pricing.Names[i], *v, now); err != nil {
				return res, err
			}
		}
		for _, pr := range plan.Proposals {
			if _, err := tx.Exec(ctx, `INSERT INTO price_proposals (model_id, backend, rate, current_per_m, proposed_per_m, litellm_key, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (model_id, backend, rate) WHERE status = 'open'
				DO UPDATE SET current_per_m = EXCLUDED.current_per_m, proposed_per_m = EXCLUDED.proposed_per_m, litellm_key = EXCLUDED.litellm_key, created_at = EXCLUDED.created_at`,
				pp.model, pp.backend, pricing.Names[pr.Rate], pr.Current, pr.Proposed, key, now); err != nil {
				return res, err
			}
			res.Proposed++
			target := fmt.Sprintf("%s on %s %s: LiteLLM moved to %s; the override is %s", pp.model, pp.backend,
				strings.ToLower(pricing.Labels[pr.Rate]), rate(&pr.Proposed), rate(&pr.Current))
			if err := auditFrom(ctx, tx, pp.tenant, SyncActor, "Proposed model price change", target, "Pricing", pp.model+"@"+pp.backend, nil, pr, "sync"); err != nil {
				return res, err
			}
		}
		if !plan.Changed {
			continue
		}
		next := PriceRow{ModelID: pp.model, Backend: pp.backend, Rates: plan.Rates, Sources: plan.Sources, From: now, To: cur.To}
		if had {
			if _, err := tx.Exec(ctx, `UPDATE model_pricing SET effective_to = $4 WHERE model_id = $1 AND backend = $2 AND effective_from = $3`,
				pp.model, pp.backend, cur.From, now); err != nil {
				return res, err
			}
		}
		action, after := "Changed model price", any(next)
		if next.Rates == (pricing.Rates{}) {
			res.Retired++
			action, after = "Retired model price", nil
		} else {
			res.Applied++
			if err := insertPrice(ctx, tx, next); err != nil {
				return res, err
			}
		}
		target := priceChange(cur, next)
		if err := auditFrom(ctx, tx, pp.tenant, SyncActor, action, target, "Pricing", pp.model+"@"+pp.backend, cur, after, "sync"); err != nil {
			return res, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO price_syncs (started_at, finished_at, applied, proposed, retired) VALUES ($1,$2,$3,$4,$5)`,
		started, now, res.Applied, res.Proposed, res.Retired); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// RecordSyncFailure notes a sync that couldn't run (the file didn't load).
func (s *Store) RecordSyncFailure(ctx context.Context, started, now time.Time, cause error) error {
	_, err := s.Config.Exec(ctx, `INSERT INTO price_syncs (started_at, finished_at, error) VALUES ($1,$2,$3)`, started, now, cause.Error())
	return err
}

// SyncRun is one row of price_syncs.
type SyncRun struct {
	Started, Finished          time.Time
	Error                      string
	Applied, Proposed, Retired int
}

// LastSyncs returns the newest sync and the newest successful one (either
// may be nil).
func (s *Store) LastSyncs(ctx context.Context) (last, lastOK *SyncRun, err error) {
	get := func(where string) (*SyncRun, error) {
		var r SyncRun
		err := s.Config.QueryRow(ctx, `SELECT started_at, finished_at, coalesce(error, ''), applied, proposed, retired FROM price_syncs `+where+` ORDER BY id DESC LIMIT 1`).
			Scan(&r.Started, &r.Finished, &r.Error, &r.Applied, &r.Proposed, &r.Retired)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return &r, err
	}
	if last, err = get(""); err != nil {
		return nil, nil, err
	}
	lastOK, err = get("WHERE error IS NULL")
	return last, lastOK, err
}

// ProposalRow is an open price_proposals row.
type ProposalRow struct {
	ID                int64
	ModelID, Backend  string
	Rate              string
	Current, Proposed float64
	LiteLLMKey        string
	Created           time.Time
}

// OpenProposals lists the proposals waiting for a decision, newest first.
func (s *Store) OpenProposals(ctx context.Context) ([]ProposalRow, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, model_id, backend, rate, current_per_m::float8, proposed_per_m::float8, litellm_key, created_at
		FROM price_proposals WHERE status = 'open' ORDER BY created_at DESC, id DESC`)
	return collect(rows, func(r pgx.Rows) (ProposalRow, error) {
		var p ProposalRow
		return p, r.Scan(&p.ID, &p.ModelID, &p.Backend, &p.Rate, &p.Current, &p.Proposed, &p.LiteLLMKey, &p.Created)
	})
}

// openProposal reads one open proposal.
func (s *Store) openProposal(ctx context.Context, id int64) (ProposalRow, error) {
	var p ProposalRow
	err := s.Config.QueryRow(ctx, `SELECT id, model_id, backend, rate, current_per_m::float8, proposed_per_m::float8, litellm_key, created_at
		FROM price_proposals WHERE id = $1 AND status = 'open'`, id).
		Scan(&p.ID, &p.ModelID, &p.Backend, &p.Rate, &p.Current, &p.Proposed, &p.LiteLLMKey, &p.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// AcceptProposal puts the proposal's rate back on LiteLLM (at the value it
// proposed, which is the last one seen), from now.
func (s *Store) AcceptProposal(ctx context.Context, tenant, actor string, id int64, now time.Time) (PriceRow, error) {
	p, err := s.openProposal(ctx, id)
	if err != nil {
		return PriceRow{}, err
	}
	r := rateByName(p.Rate)
	row, err := s.SetPrice(ctx, tenant, actor, PriceEdit{ModelID: p.ModelID, Backend: p.Backend, Set: map[pricing.Rate]*float64{r: nil}, From: now}, now)
	if err != nil {
		return row, err
	}
	// SetPrice dismissed it as touched; record it as accepted instead.
	_, err = s.Config.Exec(ctx, `UPDATE price_proposals SET status = 'accepted' WHERE id = $1`, id)
	return row, err
}

// DismissProposal keeps the override. The sync won't propose again until
// LiteLLM moves from the value it proposed.
func (s *Store) DismissProposal(ctx context.Context, tenant, actor string, id int64, now time.Time) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE price_proposals SET status = 'dismissed', decided_by = $2, decided_at = $3 WHERE id = $1 AND status = 'open'`, id, actor, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	var m, b, r string
	var cur, prop float64
	if err := tx.QueryRow(ctx, `SELECT model_id, backend, rate, current_per_m::float8, proposed_per_m::float8 FROM price_proposals WHERE id = $1`, id).Scan(&m, &b, &r, &cur, &prop); err != nil {
		return err
	}
	target := fmt.Sprintf("%s on %s %s: kept the override %s, not LiteLLM's %s", m, b, strings.ToLower(pricing.Labels[rateByName(r)]), rate(&cur), rate(&prop))
	if err := audit(ctx, tx, tenant, actor, "Dismissed model price proposal", target, "Pricing", m+"@"+b, nil, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func rateByName(n string) pricing.Rate {
	for i, x := range pricing.Names {
		if x == n {
			return pricing.Rate(i)
		}
	}
	return -1
}

// RateByName is the Rate a JSON key names, ok false for an unknown one.
func RateByName(n string) (pricing.Rate, bool) {
	r := rateByName(n)
	return r, r >= 0
}

// PriceUnpriced costs the settled receipts that had no price when they
// arrived, at their pair's row in effect now (prices), and refreshes the
// aggregates over the days it touched. Receipts whose tokens need a rate the
// row still lacks stay unpriced. It returns how many it priced.
//
// Each receipt is updated by its exact (id, ts): Timescale then decompresses
// only the batch holding it, where a filter on model and backend would
// decompress whole chunks.
func (s *Store) PriceUnpriced(ctx context.Context, prices []PriceRow, models map[string]model.Model) (int, error) {
	byPair := map[[2]string]PriceRow{}
	for _, p := range prices {
		byPair[[2]string{p.ModelID, p.Backend}] = p
	}
	type waiting struct {
		id    string
		ts    time.Time
		model string
		back  string
		tok   pricing.Tokens
	}
	rows, _ := s.Receipts.Query(ctx, `
		SELECT id, ts, resolved_model, backend, input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, reasoning_tokens
		FROM receipts WHERE cost_usd IS NULL AND status = 200 AND NOT in_flight`)
	got, err := collect(rows, func(r pgx.Rows) (waiting, error) {
		var w waiting
		return w, r.Scan(&w.id, &w.ts, &w.model, &w.back, &w.tok.Input, &w.tok.Cached, &w.tok.CacheWrite, &w.tok.Output, &w.tok.Reasoning)
	})
	if err != nil {
		return 0, err
	}
	b := &pgx.Batch{}
	var from, to time.Time
	for _, w := range got {
		row, ok := byPair[[2]string{w.model, w.back}]
		if !ok {
			continue
		}
		cost := pricing.Cost(row.Rates, w.tok)
		if cost == nil {
			continue
		}
		basis := Basis(models[w.model], row)
		basis.PricedLater = true
		b.Queue(`UPDATE receipts SET cost_usd = $3, cost_basis = $4 WHERE id = $1 AND ts = $2 AND cost_usd IS NULL`, w.id, w.ts, *cost, basis)
		if from.IsZero() || w.ts.Before(from) {
			from = w.ts
		}
		if w.ts.After(to) {
			to = w.ts
		}
	}
	if b.Len() == 0 {
		return 0, nil
	}
	if err := s.Receipts.SendBatch(ctx, b).Close(); err != nil {
		return 0, err
	}
	day := 24 * time.Hour
	return b.Len(), s.RefreshAggregates(ctx, from.Truncate(day), to.Truncate(day).Add(day))
}

// badPrice is a price write that fails validation (a 400 at the API).
type badPrice struct{ error }

// IsBadPrice reports whether err is a price write's validation failure.
func IsBadPrice(err error) bool { return errors.As(err, new(badPrice)) }
