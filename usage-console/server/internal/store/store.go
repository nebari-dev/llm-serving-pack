// Package store owns both databases: config (Postgres) and receipts
// (Postgres + TimescaleDB).
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations
var migrations embed.FS

type Store struct {
	Config   *pgxpool.Pool
	Receipts *pgxpool.Pool
}

func Open(ctx context.Context, configURL, receiptsURL string) (*Store, error) {
	c, err := pgxpool.New(ctx, configURL)
	if err != nil {
		return nil, fmt.Errorf("config db: %w", err)
	}
	r, err := pgxpool.New(ctx, receiptsURL)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("receipts db: %w", err)
	}
	for name, p := range map[string]*pgxpool.Pool{"config": c, "receipts": r} {
		if err := p.Ping(ctx); err != nil {
			c.Close()
			r.Close()
			return nil, fmt.Errorf("%s db: %w", name, err)
		}
	}
	return &Store{Config: c, Receipts: r}, nil
}

func (s *Store) Close() {
	s.Config.Close()
	s.Receipts.Close()
}

// Migrate applies any unapplied files under migrations/config and
// migrations/receipts, in name order, each in its own transaction.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := migrate(ctx, s.Config, "migrations/config"); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	applied, err := migrate(ctx, s.Receipts, "migrations/receipts")
	if err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	// A rebuilt aggregate starts empty. Without a full refresh, its policy
	// would materialize only the last few days and hide everything older.
	if slices.Contains(applied, "003_daily_backend.sql") {
		if _, err := s.Receipts.Exec(ctx, `CALL refresh_continuous_aggregate('receipts_daily', NULL, now())`); err != nil {
			return fmt.Errorf("receipts: refresh receipts_daily: %w", err)
		}
	}
	// Models a backend added before 2026-10-08 were filed under their own
	// name as family; they get ModelFamily's, once (a later edit stays).
	var done bool
	const families = "model_family.backfill"
	if err := s.Config.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, families).Scan(&done); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if !done {
		if err := s.backfillFamilies(ctx); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if _, err := s.Config.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, families); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	// Receipts from before 008 get their project id from the config database.
	// It's recorded like a migration once done, so a failed run is retried
	// at the next start.
	const backfill = "008_project_id.backfill"
	if err := s.Receipts.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, backfill).Scan(&done); err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	if !done {
		if err := s.backfillReceiptProjects(ctx); err != nil {
			return fmt.Errorf("receipts: %w", err)
		}
		if _, err := s.Receipts.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, backfill); err != nil {
			return fmt.Errorf("receipts: %w", err)
		}
	}
	return nil
}

// migrate returns the names of the files it applied.
func migrate(ctx context.Context, db *pgxpool.Pool, dir string) ([]string, error) {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var applied []string
	for _, name := range names {
		var done bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&done); err != nil {
			return nil, err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(dir + "/" + name)
		if err != nil {
			return nil, err
		}
		// No arguments, so pgx sends the file as one simple-protocol query that
		// Postgres runs as a single implicit transaction.
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			return nil, err
		}
		applied = append(applied, name)
	}
	return applied, nil
}

func collect[T any](rows pgx.Rows, scan func(pgx.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
