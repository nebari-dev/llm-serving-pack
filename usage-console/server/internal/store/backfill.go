package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// backfillDays are the UTC days from oldest's through now's, newest first;
// none when there are no receipts (oldest is zero).
func backfillDays(oldest, now time.Time) []time.Time {
	if oldest.IsZero() {
		return nil
	}
	var out []time.Time
	first := oldest.UTC().Truncate(day)
	for d := now.UTC().Truncate(day); !d.Before(first); d = d.Add(-day) {
		out = append(out, d)
	}
	return out
}

// backfillReceiptProjects gives receipts from before receipts migration 008
// their project id, from their key's project. A key never changes project,
// so that's exact; receipts with no key, or a key the config database no
// longer has, stay null. Receipts live in another database, so this runs
// here rather than in the migration.
//
// It goes a UTC day (one chunk) at a time. Updating on key_id, which isn't
// the compression segment, decompresses the chunk, and Timescale caps how
// many tuples one transaction may decompress; the cap is lifted for each
// day's transaction, and the compression policy recompresses it later.
func (s *Store) backfillReceiptProjects(ctx context.Context) error {
	rows, _ := s.Config.Query(ctx, `SELECT id, project_id FROM api_keys`)
	var keys, projects []string
	if _, err := collect(rows, func(r pgx.Rows) (struct{}, error) {
		var k, p string
		err := r.Scan(&k, &p)
		keys, projects = append(keys, k), append(projects, p)
		return struct{}{}, err
	}); err != nil {
		return err
	}
	var oldest *time.Time
	if err := s.Receipts.QueryRow(ctx, `SELECT min(ts) FROM receipts WHERE project_id IS NULL`).Scan(&oldest); err != nil || oldest == nil || len(keys) == 0 {
		return err
	}
	var limit *string
	if err := s.Receipts.QueryRow(ctx, `SELECT current_setting('timescaledb.max_tuples_decompressed_per_dml_transaction', true)`).Scan(&limit); err != nil {
		return err
	}
	for _, d := range backfillDays(*oldest, time.Now()) {
		err := pgx.BeginFunc(ctx, s.Receipts, func(tx pgx.Tx) error {
			if limit != nil {
				if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `
				UPDATE receipts r SET project_id = m.project_id
				FROM unnest($1::text[], $2::text[]) AS m(key_id, project_id)
				WHERE r.ts >= $3 AND r.ts < $4 AND r.project_id IS NULL AND r.key_id = m.key_id`,
				keys, projects, d, d.Add(day))
			return err
		})
		if err != nil {
			return fmt.Errorf("backfill project ids for %s: %w", d.Format(time.DateOnly), err)
		}
	}
	return nil
}
