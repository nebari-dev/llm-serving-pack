// Package store owns both databases: config (Postgres) and receipts
// (Postgres + TimescaleDB).
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
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
	if err := migrate(ctx, s.Config, "migrations/config"); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := migrate(ctx, s.Receipts, "migrations/receipts"); err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, db *pgxpool.Pool, dir string) error {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(dir + "/" + name)
		if err != nil {
			return err
		}
		// No arguments, so pgx sends the file as one simple-protocol query that
		// Postgres runs as a single implicit transaction.
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			return err
		}
	}
	return nil
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
