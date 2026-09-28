package store

import (
	"context"
	"time"
)

// Retention is the receipts database's policy as Timescale's jobs hold it,
// not as the spec says it should be.
type Retention struct {
	// HotDays is the raw receipts' drop_after; nil when no policy drops them.
	HotDays           *float64
	CompressAfterDays *float64
	Aggregates        []AggregateRetention
	// OldestReceipt is the oldest raw receipt the tenant still has.
	OldestReceipt *time.Time
}

type AggregateRetention struct {
	Name          string
	DropAfterDays *float64
}

const policyDays = `(SELECT extract(epoch FROM (j.config->>$2)::interval) / 86400
	FROM timescaledb_information.jobs j WHERE j.proc_name = $1 AND j.hypertable_name = $3 LIMIT 1)`

func (s *Store) Retention(ctx context.Context, tenant string) (Retention, error) {
	var r Retention
	if err := s.Receipts.QueryRow(ctx, `SELECT `+policyDays, "policy_retention", "drop_after", "receipts").Scan(&r.HotDays); err != nil {
		return r, err
	}
	if err := s.Receipts.QueryRow(ctx, `SELECT `+policyDays, "policy_compression", "compress_after", "receipts").Scan(&r.CompressAfterDays); err != nil {
		return r, err
	}
	if err := s.Receipts.QueryRow(ctx, `SELECT min(ts) FROM receipts WHERE tenant_id = $1`, tenant).Scan(&r.OldestReceipt); err != nil {
		return r, err
	}
	rows, err := s.Receipts.Query(ctx, `SELECT view_name, materialization_hypertable_name FROM timescaledb_information.continuous_aggregates ORDER BY view_name`)
	if err != nil {
		return r, err
	}
	type cagg struct{ view, mat string }
	var cs []cagg
	for rows.Next() {
		var c cagg
		if err := rows.Scan(&c.view, &c.mat); err != nil {
			rows.Close()
			return r, err
		}
		cs = append(cs, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return r, err
	}
	r.Aggregates = []AggregateRetention{}
	for _, c := range cs {
		a := AggregateRetention{Name: c.view}
		if err := s.Receipts.QueryRow(ctx, `SELECT `+policyDays, "policy_retention", "drop_after", c.mat).Scan(&a.DropAfterDays); err != nil {
			return r, err
		}
		r.Aggregates = append(r.Aggregates, a)
	}
	return r, nil
}

// Audited writes an audit row and runs apply in the same transaction,
// committing only when apply succeeds: the log never records a change that
// didn't happen. (If the commit itself fails after apply, the change stands
// unlogged; the caller reports the error.)
func (s *Store) Audited(ctx context.Context, tenant, actor, action, target, kind string, before, after any, apply func() error) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := audit(ctx, tx, tenant, actor, action, target, kind, "", before, after); err != nil {
		return err
	}
	if err := apply(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
