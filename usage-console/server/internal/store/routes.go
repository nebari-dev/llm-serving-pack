package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/routing"
)

// RouteETag is a route's version for If-Match: everything a write can change.
func RouteETag(r model.Route) string {
	return ETag([]any{r.Name, r.Match, r.Targets, r.Fallback, r.CaptureContent})
}

const routeCols = `name, match, targets, fallback, capture_content`

func scanRoute(r pgx.Row) (model.Route, error) {
	var x model.Route
	var match, targets, fallback []byte
	err := r.Scan(&x.Name, &match, &targets, &fallback, &x.CaptureContent)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	if err := errors.Join(json.Unmarshal(match, &x.Match), json.Unmarshal(targets, &x.Targets), json.Unmarshal(fallback, &x.Fallback)); err != nil {
		return x, err
	}
	normalizeRoute(&x)
	x.ETag = RouteETag(x)
	return x, nil
}

// normalizeRoute gives a route's lists their empty values, so JSON says []
// rather than null and two copies of a route compare equal.
func normalizeRoute(r *model.Route) {
	if r.Match.Models == nil {
		r.Match.Models = []string{}
	}
	if r.Match.Headers == nil {
		r.Match.Headers = []model.HeaderMatch{}
	}
	if r.Targets == nil {
		r.Targets = []model.RouteTarget{}
	}
	if r.Fallback == nil {
		r.Fallback = []model.RouteTarget{}
	}
	// One target takes all the traffic; a weight on it means nothing.
	if len(r.Targets) == 1 {
		r.Targets[0].Weight = 0
	}
}

// Routes is the tenant's desired routing, in rule order.
func (s *Store) Routes(ctx context.Context, tenant string) ([]model.Route, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+routeCols+` FROM routes WHERE tenant_id = $1 ORDER BY ordinal, name`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Route, error) { return scanRoute(r) })
}

// routeSummary is a route for the audit log: "gpt-5-mini, gpt-5.5 →
// openai-prod, then bedrock-eu".
func routeSummary(r model.Route) string {
	var to []string
	for _, t := range r.Targets {
		s := t.Backend
		if t.Model != "" {
			s += " as " + t.Model
		}
		if len(r.Targets) > 1 {
			s += fmt.Sprintf(" (%d)", t.Weight)
		}
		to = append(to, s)
	}
	out := r.Name + " · " + strings.Join(r.Match.Models, ", ")
	for _, h := range r.Match.Headers {
		out += fmt.Sprintf(" if %s = %s", h.Name, h.Value)
	}
	out += " → " + strings.Join(to, ", ")
	for _, t := range r.Fallback {
		out += ", then " + t.Backend
	}
	return out
}

func marshalRoute(r model.Route) (match, targets, fallback []byte) {
	match, _ = json.Marshal(r.Match)
	targets, _ = json.Marshal(r.Targets)
	fallback, _ = json.Marshal(r.Fallback)
	return
}

// lockRoutes takes the tenant's routes for update, in rule order.
func lockRoutes(ctx context.Context, tx pgx.Tx, tenant string) ([]model.Route, error) {
	rows, _ := tx.Query(ctx, `SELECT `+routeCols+` FROM routes WHERE tenant_id = $1 ORDER BY ordinal, name FOR UPDATE`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Route, error) { return scanRoute(r) })
}

// CreateRoute adds a route ahead of any catch-all (routing.InsertAt).
func (s *Store) CreateRoute(ctx context.Context, tenant, actor string, r model.Route) (model.Route, error) {
	normalizeRoute(&r)
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer tx.Rollback(ctx)
	routes, err := lockRoutes(ctx, tx, tenant)
	if err != nil {
		return r, err
	}
	at := routing.InsertAt(routes, r)
	if _, err := tx.Exec(ctx, `
		UPDATE routes SET ordinal = o.n + 1
		FROM (SELECT name, row_number() OVER (ORDER BY ordinal, name) - 1 AS n FROM routes WHERE tenant_id = $1) o
		WHERE routes.tenant_id = $1 AND routes.name = o.name AND o.n >= $2`, tenant, at); err != nil {
		return r, err
	}
	m, t, f := marshalRoute(r)
	r, err = scanRoute(tx.QueryRow(ctx, `
		INSERT INTO routes (tenant_id, name, ordinal, match, targets, fallback, capture_content) VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+routeCols, tenant, r.Name, at, m, t, f, r.CaptureContent))
	if err != nil {
		return r, uniqueConflict(err)
	}
	if err := audit(ctx, tx, tenant, actor, "Created route", routeSummary(r), "Route", r.Name, nil, r); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// UpdateRoute replaces a route's match, targets and fallback; its name and
// place are fixed. An edit that changes nothing writes no audit row.
func (s *Store) UpdateRoute(ctx context.Context, tenant, actor, ifMatch string, next model.Route) (model.Route, error) {
	normalizeRoute(&next)
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return next, err
	}
	defer tx.Rollback(ctx)
	was, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeCols+` FROM routes WHERE tenant_id = $1 AND name = $2 FOR UPDATE`, tenant, next.Name))
	if err != nil {
		return was, err
	}
	if ifMatch != "" && ifMatch != was.ETag {
		return was, &StaleError{Current: was}
	}
	next.ETag = RouteETag(next)
	if next.ETag == was.ETag {
		return was, nil
	}
	m, t, f := marshalRoute(next)
	if _, err := tx.Exec(ctx, `UPDATE routes SET match = $3, targets = $4, fallback = $5, capture_content = $6 WHERE tenant_id = $1 AND name = $2`,
		tenant, next.Name, m, t, f, next.CaptureContent); err != nil {
		return was, err
	}
	if err := audit(ctx, tx, tenant, actor, "Changed route", routeSummary(next), "Route", next.Name, was, next); err != nil {
		return was, err
	}
	return next, tx.Commit(ctx)
}

func (s *Store) DeleteRoute(ctx context.Context, tenant, actor, name, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	was, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeCols+` FROM routes WHERE tenant_id = $1 AND name = $2 FOR UPDATE`, tenant, name))
	if err != nil {
		return err
	}
	if ifMatch != "" && ifMatch != was.ETag {
		return &StaleError{Current: was}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM routes WHERE tenant_id = $1 AND name = $2`, tenant, name); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted route", routeSummary(was), "Route", name, was, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RoutingApply is one apply of the desired routing to the gateway.
type RoutingApply struct {
	At      int64            `json:"at"` // epoch ms
	Actor   string           `json:"actor"`
	OK      bool             `json:"ok"`
	Error   string           `json:"error,omitempty"`
	Changes []routing.Change `json:"changes"`
	// Attempted is what the apply tried (api.attempted), for a failed one's
	// sync states.
	Attempted map[string]string `json:"-"`
}

// RecordApply logs an apply, what it tried, and its audit row. Changes keep
// their kind, name and change, not their diffs.
// routes is what a successful apply put in front of the gateway (nil when it failed).
func (s *Store) RecordApply(ctx context.Context, tenant, actor string, ok bool, applyErr string, changes []routing.Change, tried map[string]string, routes []model.Route) error {
	brief := make([]routing.Change, len(changes))
	var names []string
	for i, c := range changes {
		brief[i] = routing.Change{Kind: c.Kind, Name: c.Name, Change: c.Change}
		names = append(names, c.Change+" "+c.Kind+" "+c.Name)
	}
	cj, _ := json.Marshal(brief)
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var errCol *string
	if !ok {
		errCol = &applyErr
	}
	tj, _ := json.Marshal(tried)
	var rj []byte
	if ok && routes != nil {
		rj, _ = json.Marshal(routes)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routing_applies (tenant_id, actor, ok, error, changes, attempted, routes) VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenant, actor, ok, errCol, cj, tj, rj); err != nil {
		return err
	}
	action, target := "Applied routing", fmt.Sprintf("%d %s: %s", len(changes), plural(len(changes), "change", "changes"), strings.Join(names, ", "))
	if !ok {
		action = "Routing apply failed"
	}
	if err := audit(ctx, tx, tenant, actor, action, target, "Routing", "", nil, map[string]any{"changes": brief, "error": errCol}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// LastApply is the tenant's latest apply, or nil if there's been none.
func (s *Store) LastApply(ctx context.Context, tenant string) (*RoutingApply, error) {
	var a RoutingApply
	var ts time.Time
	var errCol *string
	var changes, tried []byte
	err := s.Config.QueryRow(ctx, `SELECT ts, actor, ok, error, changes, attempted FROM routing_applies WHERE tenant_id = $1 ORDER BY ts DESC, id DESC LIMIT 1`, tenant).
		Scan(&ts, &a.Actor, &a.OK, &errCol, &changes, &tried)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.At = ts.UnixMilli()
	if errCol != nil {
		a.Error = *errCol
	}
	return &a, errors.Join(json.Unmarshal(changes, &a.Changes), json.Unmarshal(tried, &a.Attempted))
}
