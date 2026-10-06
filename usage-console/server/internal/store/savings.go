package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/pricing"
)

// SavingsCell sums one group of served requests for the savings analysis
// (§7.5.5): by key, what the client asked for and what ran, the price period
// the requests fell in, how big they were, whether the answer was short, and
// whether they had a price.
type SavingsCell struct {
	KeyID, Requested, Resolved string
	// Period is the 1-based index of the price period (SavingsCells' periods)
	// the requests started in.
	Period int
	// Fit is width_bucket(input + output, fits): how many of the fits
	// thresholds the request size reached.
	Fit int
	// Short: output (reasoning included) was at most the output limit.
	Short bool
	// Priced: the requests had a cost. Unpriced ones have USD 0 here, which
	// is no price, not free.
	Priced   bool
	Requests int
	// Tokens are the group's sums. Cached and cache writes are within
	// Input, reasoning within Output, as on a receipt.
	Tokens pricing.Tokens
	USD    float64
	First  time.Time
}

// SavingsCells groups the tenant's served requests in [from, to) for the
// savings analysis. It reads raw receipts, because the aggregates don't keep
// output length; raw receipts go back 30 days (§4.6). periods are the
// ascending starts of the price periods, the first being from; fits are
// ascending request-size thresholds.
func (s *Store) SavingsCells(ctx context.Context, tenant string, from, to time.Time, periods []time.Time, fits []int, outputLimit int) ([]SavingsCell, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT key_id, requested_model, resolved_model,
		       width_bucket(ts, $4::timestamptz[]), width_bucket(input_tokens + output_tokens, $5::int[]),
		       output_tokens <= $6, cost_usd IS NOT NULL, count(*)::int,
		       sum(greatest(input_tokens - cached_input_tokens - cache_write_tokens, 0))::bigint,
		       sum(cached_input_tokens)::bigint, sum(cache_write_tokens)::bigint,
		       sum(greatest(output_tokens - reasoning_tokens, 0))::bigint, sum(reasoning_tokens)::bigint,
		       coalesce(sum(cost_usd), 0)::float8, min(ts)
		FROM receipts
		WHERE tenant_id = $1 AND ts >= $2 AND ts < $3 AND status = 200 AND NOT in_flight
		GROUP BY 1, 2, 3, 4, 5, 6, 7`, tenant, from, to, periods, fits, outputLimit)
	return collect(rows, func(r pgx.Rows) (SavingsCell, error) {
		var c SavingsCell
		var uncached, visible int64
		var cached, writes, reasoning int64
		err := r.Scan(&c.KeyID, &c.Requested, &c.Resolved, &c.Period, &c.Fit, &c.Short, &c.Priced, &c.Requests,
			&uncached, &cached, &writes, &visible, &reasoning, &c.USD, &c.First)
		c.Tokens = pricing.Tokens{Input: int(uncached + cached + writes), Cached: int(cached), CacheWrite: int(writes),
			Output: int(visible + reasoning), Reasoning: int(reasoning)}
		return c, err
	})
}
