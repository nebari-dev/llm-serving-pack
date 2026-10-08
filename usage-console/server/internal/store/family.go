package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// ModelFamily is the family a model belongs to, for a model a backend adds
// to the catalog: Savings compares a model only with cheaper ones of the
// same family. Anthropic's models are "claude" and OpenAI's gpt-* are "gpt";
// otherwise it's the name's words up to the first that carries a version,
// or that word's letters ("llama-3.3-70b" is "llama", "smollm2" "smollm"),
// after any vendor path ("openai/…") and tag (":360M"). Editable on Models.
func ModelFamily(provider, id string) string {
	name := strings.ToLower(id)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	switch {
	case provider == "Anthropic" || strings.HasPrefix(name, "claude"):
		return "claude"
	case strings.HasPrefix(name, "gpt-"):
		return "gpt"
	}
	var keep []string
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if strings.IndexFunc(w, unicode.IsDigit) < 0 {
			keep = append(keep, w)
			continue
		}
		if len(keep) == 0 {
			if letters := strings.TrimRightFunc(w[:strings.IndexFunc(w, unicode.IsDigit)], func(r rune) bool { return !unicode.IsLetter(r) }); letters != "" {
				keep = append(keep, letters)
			}
		}
		break
	}
	if len(keep) == 0 {
		return name
	}
	return strings.Join(keep, "-")
}

// backfillFamilies gives each model filed under its own name (as a backend
// added them before ModelFamily) the family ModelFamily says.
func (s *Store) backfillFamilies(ctx context.Context) error {
	rows, err := s.Config.Query(ctx, `SELECT id, provider FROM model_catalog WHERE family = id`)
	if err != nil {
		return err
	}
	var ids, providers []string
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		ids, providers = append(ids, id), append(providers, p)
	}
	rows.Close()
	for i, id := range ids {
		if _, err := s.Config.Exec(ctx, `UPDATE model_catalog SET family = $2 WHERE id = $1`, id, ModelFamily(providers[i], id)); err != nil {
			return err
		}
	}
	return nil
}

// SetModelFamily changes a model's family if it's still from, with its audit
// row: compare-and-set, so an edit made meanwhile isn't overwritten.
func (s *Store) SetModelFamily(ctx context.Context, tenant, actor, id, from, to string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cur string
	if err := tx.QueryRow(ctx, `SELECT family FROM model_catalog WHERE id = $1 FOR UPDATE`, id).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if cur != from {
		return &StaleError{Current: map[string]string{"id": id, "family": cur}}
	}
	if from == to {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE model_catalog SET family = $2 WHERE id = $1`, id, to); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Changed model family", fmt.Sprintf("%s %s → %s", id, from, to), "Model", id, map[string]string{"family": from}, map[string]string{"family": to}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
