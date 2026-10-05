package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/routing"
)

// BackendETag is a backend's version for If-Match: what an edit can change.
// Its key is replaced without one, like other key writes (decisions §7).
func BackendETag(b model.Backend) string {
	var e *model.BackendEndpoint
	if b.Endpoint != nil {
		x := *b.Endpoint
		x.BaseURL, x.KeyVersion = "", ""
		e = &x
	}
	return ETag([]any{b.Name, b.Provider, b.Region, b.Models, e})
}

const backendCols = `name, provider, region, provenance, sync_state, coalesce(source_ref, ''), models, health, p50_ms, error_rate::float8, capture_content,
	schema, coalesce(prefix, ''), host, port, tls, coalesce(api_key_env, ''), key_prefix, key_set_at, tested_at, test_ok, coalesce(test_message, '')`

func scanBackend(r pgx.Row) (model.Backend, error) {
	var b model.Backend
	var schema, host, port, keyPrefix *string
	var keySetAt, testedAt *time.Time
	var testOK *bool
	var testMsg string
	var e model.BackendEndpoint
	err := r.Scan(&b.Name, &b.Provider, &b.Region, &b.Provenance, &b.Sync, &b.Source, &b.Models, &b.Health, &b.P50, &b.ErrorRate, &b.CaptureContent,
		&schema, &e.Prefix, &host, &port, &e.TLS, &e.APIKeyEnv, &keyPrefix, &keySetAt, &testedAt, &testOK, &testMsg)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if host != nil {
		e.Schema, e.Host, e.Port = *schema, *host, *port
		e.BaseURL = routing.BaseURL(e)
		if keySetAt != nil {
			e.KeyVersion = keySetAt.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		b.Endpoint = &e
	}
	if keyPrefix != nil && keySetAt != nil {
		b.Key = &model.ProviderKey{Prefix: *keyPrefix, SetAt: keySetAt.UnixMilli()}
	}
	if testedAt != nil && testOK != nil {
		b.LastTest = &model.BackendTest{At: testedAt.UnixMilli(), OK: *testOK, Message: testMsg}
	}
	b.ETag = BackendETag(b)
	return b, nil
}

func (s *Store) Backends(ctx context.Context, tenant string) ([]model.Backend, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+backendCols+` FROM backends WHERE tenant_id = $1 ORDER BY ordinal`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Backend, error) { return scanBackend(r) })
}

func (s *Store) Backend(ctx context.Context, tenant, name string) (model.Backend, error) {
	return scanBackend(s.Config.QueryRow(ctx, `SELECT `+backendCols+` FROM backends WHERE tenant_id = $1 AND name = $2`, tenant, name))
}

// backendSummary is a backend for the audit log, naming its key by prefix.
func backendSummary(b model.Backend) string {
	parts := []string{b.Name, b.Provider}
	if b.Endpoint != nil {
		parts = append(parts, routing.BaseURL(*b.Endpoint))
	}
	parts = append(parts, strings.Join(b.Models, ", "))
	if b.Key != nil {
		parts = append(parts, "key "+b.Key.Prefix+"…")
	}
	return strings.Join(parts, " · ")
}

// backendAudit is a backend's before or after in the audit log.
func backendAudit(b model.Backend) map[string]any {
	out := map[string]any{"name": b.Name, "provider": b.Provider, "region": b.Region, "models": b.Models}
	if b.Endpoint != nil {
		out["baseUrl"] = routing.BaseURL(*b.Endpoint)
		out["apiKeyEnv"] = b.Endpoint.APIKeyEnv
	}
	if b.Key != nil {
		out["keyPrefix"] = b.Key.Prefix
	}
	return out
}

// ensureModels adds the models the catalog doesn't know, as the backend's
// provider's, with no context length and no price: pricing shows the pair
// as "no price" until a rate or a LiteLLM key is set (decisions §1).
func ensureModels(ctx context.Context, tx pgx.Tx, models []string, provider string) error {
	for _, m := range models {
		if _, err := tx.Exec(ctx, `INSERT INTO model_catalog (id, display, provider, family, context) VALUES ($1, $1, $2, $1, 0) ON CONFLICT (id) DO NOTHING`, m, provider); err != nil {
			return err
		}
	}
	return nil
}

// NewKey is a provider key to store with a backend write: the reference it's
// stored under, its prefix, and Put, which writes it to the key store. Put
// runs inside the transaction, so a key that can't be stored saves nothing.
type NewProviderKey struct {
	Ref, Prefix string
	Put         func() error
}

// CreateBackend adds a backend after the others, with its key if one is
// given. Models the catalog doesn't know are added to it.
func (s *Store) CreateBackend(ctx context.Context, tenant, actor string, b model.Backend, key *NewProviderKey) (model.Backend, error) {
	e := b.Endpoint
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return b, err
	}
	defer tx.Rollback(ctx)
	if err := ensureModels(ctx, tx, b.Models, b.Provider); err != nil {
		return b, err
	}
	var ref, prefix *string
	if key != nil {
		ref, prefix = &key.Ref, &key.Prefix
	}
	b, err = scanBackend(tx.QueryRow(ctx, `
		INSERT INTO backends (name, tenant_id, ordinal, provider, region, provenance, sync_state, models, health, p50_ms, error_rate,
		                      schema, prefix, host, port, tls, api_key_env, key_prefix, key_set_at)
		VALUES ($1, $2, (SELECT coalesce(max(ordinal), 0) + 1 FROM backends WHERE tenant_id = $2), $3, $4, 'console', 'synced', $5, 'healthy', 0, 0,
		        $6, $7, $8, $9, $10, $11, $12, CASE WHEN $12::text IS NULL THEN NULL ELSE now() END)
		RETURNING `+backendCols, b.Name, tenant, b.Provider, b.Region, b.Models, e.Schema, e.Prefix, e.Host, e.Port, e.TLS, ref, prefix))
	if err != nil {
		return b, uniqueConflict(err)
	}
	if err := audit(ctx, tx, tenant, actor, "Created backend", backendSummary(b), "Backend", b.Name, nil, backendAudit(b)); err != nil {
		return b, err
	}
	if key != nil {
		if err := key.Put(); err != nil {
			return b, err
		}
	}
	return b, tx.Commit(ctx)
}

// UpdateBackend replaces a backend's provider, region, models and endpoint;
// its name and key reference are fixed. An edit that changes nothing writes
// no audit row.
func (s *Store) UpdateBackend(ctx context.Context, tenant, actor, ifMatch string, next model.Backend) (model.Backend, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return next, err
	}
	defer tx.Rollback(ctx)
	was, err := scanBackend(tx.QueryRow(ctx, `SELECT `+backendCols+` FROM backends WHERE tenant_id = $1 AND name = $2 FOR UPDATE`, tenant, next.Name))
	if err != nil {
		return was, err
	}
	if ifMatch != "" && ifMatch != was.ETag {
		return was, &StaleError{Current: was}
	}
	e := *next.Endpoint
	if was.Endpoint != nil {
		e.APIKeyEnv = was.Endpoint.APIKeyEnv
	}
	next.Endpoint = &e
	if BackendETag(next) == was.ETag {
		return was, nil
	}
	if err := ensureModels(ctx, tx, next.Models, next.Provider); err != nil {
		return was, err
	}
	b, err := scanBackend(tx.QueryRow(ctx, `
		UPDATE backends SET provider = $3, region = $4, models = $5, schema = $6, prefix = $7, host = $8, port = $9, tls = $10
		WHERE tenant_id = $1 AND name = $2 RETURNING `+backendCols,
		tenant, next.Name, next.Provider, next.Region, next.Models, e.Schema, e.Prefix, e.Host, e.Port, e.TLS))
	if err != nil {
		return was, err
	}
	if err := audit(ctx, tx, tenant, actor, "Changed backend", backendSummary(b), "Backend", b.Name, backendAudit(was), backendAudit(b)); err != nil {
		return was, err
	}
	return b, tx.Commit(ctx)
}

// inUse is a backend a route still sends to (409).
type inUse string

func (e inUse) Error() string        { return string(e) }
func (e inUse) Is(target error) bool { return target == ErrConflict }

// DeleteBackend removes a backend no route sends to, and returns it (its key
// reference is the caller's to clear). Its prices stay, as history.
func (s *Store) DeleteBackend(ctx context.Context, tenant, actor, name, ifMatch string) (model.Backend, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return model.Backend{}, err
	}
	defer tx.Rollback(ctx)
	was, err := scanBackend(tx.QueryRow(ctx, `SELECT `+backendCols+` FROM backends WHERE tenant_id = $1 AND name = $2 FOR UPDATE`, tenant, name))
	if err != nil {
		return was, err
	}
	if ifMatch != "" && ifMatch != was.ETag {
		return was, &StaleError{Current: was}
	}
	ref, _ := json.Marshal([]map[string]string{{"backend": name}})
	rows, _ := tx.Query(ctx, `SELECT name FROM routes WHERE tenant_id = $1 AND (targets @> $2 OR fallback @> $2) ORDER BY ordinal FOR UPDATE`, tenant, ref)
	routes, err := collect(rows, func(r pgx.Rows) (string, error) {
		var n string
		return n, r.Scan(&n)
	})
	if err != nil {
		return was, err
	}
	if len(routes) > 0 {
		return was, inUse(plural(len(routes), "route ", "routes ") + strings.Join(routes, ", ") + " " + plural(len(routes), "sends", "send") + " to " + name + "; change or delete " + plural(len(routes), "it", "them") + " first")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM backends WHERE tenant_id = $1 AND name = $2`, tenant, name); err != nil {
		return was, err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted backend", backendSummary(was), "Backend", name, backendAudit(was), nil); err != nil {
		return was, err
	}
	return was, tx.Commit(ctx)
}

// SetBackendKey records a new provider key for a backend: its prefix and
// when it was set, under key.Ref (the backend's existing reference, if it has
// one). The audit row names the backend and the prefix, never the key.
func (s *Store) SetBackendKey(ctx context.Context, tenant, actor, name string, key NewProviderKey) (model.Backend, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return model.Backend{}, err
	}
	defer tx.Rollback(ctx)
	was, err := scanBackend(tx.QueryRow(ctx, `SELECT `+backendCols+` FROM backends WHERE tenant_id = $1 AND name = $2 FOR UPDATE`, tenant, name))
	if err != nil {
		return was, err
	}
	if was.Endpoint == nil {
		return was, inUse(name + " has no endpoint, so the gateway has nowhere to send a key")
	}
	b, err := scanBackend(tx.QueryRow(ctx, `
		UPDATE backends SET api_key_env = $3, key_prefix = $4, key_set_at = greatest(now(), coalesce(key_set_at, now()) + interval '1 millisecond')
		WHERE tenant_id = $1 AND name = $2 RETURNING `+backendCols, tenant, name, key.Ref, key.Prefix))
	if err != nil {
		return was, err
	}
	action := "Set provider key"
	if was.Endpoint.APIKeyEnv != "" {
		action = "Replaced provider key"
	}
	before := map[string]any{"apiKeyEnv": was.Endpoint.APIKeyEnv}
	if was.Key != nil {
		before["keyPrefix"] = was.Key.Prefix
	}
	if err := audit(ctx, tx, tenant, actor, action, name+" · key "+key.Prefix+"…", "Backend", name, before, map[string]any{"apiKeyEnv": key.Ref, "keyPrefix": key.Prefix}); err != nil {
		return was, err
	}
	if err := key.Put(); err != nil {
		return was, err
	}
	return b, tx.Commit(ctx)
}

// RecordBackendTest keeps a saved backend's last connection test.
func (s *Store) RecordBackendTest(ctx context.Context, tenant, name string, t model.BackendTest) error {
	tag, err := s.Config.Exec(ctx, `UPDATE backends SET tested_at = $3, test_ok = $4, test_message = $5 WHERE tenant_id = $1 AND name = $2`,
		tenant, name, time.UnixMilli(t.At), t.OK, t.Message)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
