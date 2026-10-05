package store

import (
	"context"
	"time"

	"github.com/jbouder/stargate/server/internal/pricing"
)

// PairFacts is what LiteLLM's entry for a (model, backend) says besides price.
type PairFacts struct {
	Model, Backend string
	Modalities     []string
	Deprecation    string // YYYY-MM-DD, "" if none
}

// SaveFacts keeps the facts for every LiteLLM key a pair is priced from.
func (s *Store) SaveFacts(ctx context.Context, facts map[string]pricing.Facts, now time.Time) error {
	rows, err := s.Config.Query(ctx, `SELECT DISTINCT litellm_key FROM price_sources`)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	rows.Close()
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM litellm_facts`); err != nil {
		return err
	}
	for _, k := range keys {
		f, ok := facts[k]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO litellm_facts VALUES ($1, $2, NULLIF($3, '')::date, $4)`, k, f.Modalities, f.Deprecation, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ModelFacts are the saved facts per (model, backend), by its LiteLLM key.
func (s *Store) ModelFacts(ctx context.Context) ([]PairFacts, error) {
	rows, err := s.Config.Query(ctx, `
		SELECT p.model_id, p.backend, f.modalities, coalesce(to_char(f.deprecation_date, 'YYYY-MM-DD'), '')
		FROM price_sources p JOIN litellm_facts f USING (litellm_key)
		ORDER BY p.model_id, p.backend`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PairFacts
	for rows.Next() {
		var f PairFacts
		if err := rows.Scan(&f.Model, &f.Backend, &f.Modalities, &f.Deprecation); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
