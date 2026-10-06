package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/routing"
	"github.com/jbouder/stargate/server/internal/store"
)

// Routing (spec §4.4, §7.5.6): routes and backends in Postgres are the desired
// state. GET /routing compiles them and diffs that against what the gateway
// runs; POST /routing/apply hands it to the Applier. Drift, adopt and
// provenance wait on who owns the target's resources (docs/backend-decisions.md).

// NotReconciled is the sync state when no applier is configured: nothing puts
// routing in front of the gateway or says what it runs.
const NotReconciled = "not_reconciled"

// applyTimeout bounds an apply, rollback included. aigw takes a few seconds to
// start, more when it has to fetch Envoy.
const applyTimeout = 3 * time.Minute

// RouteView is a route with the AIGatewayRoute rule it compiles to.
type RouteView struct {
	model.Route
	YAML string `json:"yaml"`
}

// RoutingPlan is what an apply would change, and whether one can run.
type RoutingPlan struct {
	// Target is where an apply goes, as the applier puts it.
	Target   string `json:"target"`
	CanApply bool   `json:"canApply"`
	Reason   string `json:"reason,omitempty"`
	// ETag names this plan: desired and running both. Apply takes it as
	// If-Match, so what's applied is what was reviewed.
	ETag      string              `json:"etag"`
	Changes   []routing.Change    `json:"changes"`
	YAML      string              `json:"yaml"` // the desired routing, for Export
	LastApply *store.RoutingApply `json:"lastApply,omitempty"`
	desired   []routing.Object
}

// withRouteSync sets each route's sync: synced when the gateway runs its
// rule as it is; failed when the last apply failed trying this version of it
// (failed maps "route/<name>" to the version tried); else pending.
func withRouteSync(rs []model.Route, bs []model.Backend, running []routing.Object, failed map[string]string) []model.Route {
	for i := range rs {
		switch {
		case routing.RouteInSync(running, rs[i], bs):
			rs[i].Sync = "synced"
		case tried(failed, "route/"+rs[i].Name, rs[i].ETag):
			rs[i].Sync = "failed"
		default:
			rs[i].Sync = "pending"
		}
	}
	return rs
}

func withBackendSync(bs []model.Backend, running []routing.Object, failed map[string]string) []model.Backend {
	for i := range bs {
		bs[i].YAML = routing.BackendYAML(bs[i])
		switch {
		case bs[i].Endpoint == nil:
			bs[i].Sync = "no_endpoint"
		case routing.BackendInSync(running, bs[i]):
			bs[i].Sync = "synced"
		case tried(failed, "backend/"+bs[i].Name, store.BackendETag(bs[i])):
			bs[i].Sync = "failed"
		default:
			bs[i].Sync = "pending"
		}
	}
	return bs
}

// tried is whether a failed apply tried this version of the object.
func tried(failed map[string]string, key, version string) bool {
	v, ok := failed[key]
	return ok && v == version
}

// attempted is what an apply of the desired state tries: each route and
// backend the gateway doesn't run as it is, at its current version.
func attempted(running []routing.Object, rs []model.Route, bs []model.Backend) map[string]string {
	out := map[string]string{}
	for _, r := range rs {
		if !routing.RouteInSync(running, r, bs) {
			out["route/"+r.Name] = r.ETag
		}
	}
	for _, b := range bs {
		if b.Endpoint != nil && !routing.BackendInSync(running, b) {
			out["backend/"+b.Name] = store.BackendETag(b)
		}
	}
	return out
}

// observed is what the gateway runs and, if the last apply failed, what it
// tried (nil otherwise). With no applier, ok is false.
func (s *Server) observed(ctx context.Context, t string) (running []routing.Object, failed map[string]string, ok bool, err error) {
	if s.Routing == nil {
		return nil, nil, false, nil
	}
	if running, err = s.Routing.Running(ctx); err != nil {
		return nil, nil, false, err
	}
	last, err := s.Store.LastApply(ctx, t)
	if last != nil && !last.OK {
		failed = last.Attempted
	}
	return running, failed, true, err
}

func (s *Server) routeViews(ctx context.Context, t string) ([]RouteView, error) {
	rs, err := s.Store.Routes(ctx, t)
	if err != nil {
		return nil, err
	}
	bs, err := s.Store.Backends(ctx, t)
	if err != nil {
		return nil, err
	}
	running, failed, ok, err := s.observed(ctx, t)
	if err != nil {
		return nil, err
	}
	if ok {
		rs = withRouteSync(rs, bs, running, failed)
	}
	out := make([]RouteView, len(rs))
	for i, r := range rs {
		if !ok {
			r.Sync = NotReconciled
		}
		out[i] = RouteView{Route: r, YAML: routing.RuleYAML(r, bs)}
	}
	return out, nil
}

func (s *Server) routes(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.routeViews(r.Context(), t)
}

func (s *Server) routeView(w http.ResponseWriter, ctx context.Context, t, name string) (RouteView, error) {
	vs, err := s.routeViews(ctx, t)
	if err != nil {
		return RouteView{}, err
	}
	i := slices.IndexFunc(vs, func(v RouteView) bool { return v.Name == name })
	if i < 0 {
		return RouteView{}, store.ErrNotFound
	}
	w.Header().Set("ETag", vs[i].ETag)
	return vs[i], nil
}

// readRoute takes {name, match: {models, headers}, targets, fallback} and
// checks it against the backends and the other routes.
func (s *Server) readRoute(r *http.Request, t, name string) (model.Route, []model.Route, []model.Backend, error) {
	var in model.Route
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return in, nil, nil, badRequest("invalid JSON body")
	}
	if name != "" {
		in.Name = name
	}
	in.Name = strings.TrimSpace(in.Name)
	for i, m := range in.Match.Models {
		in.Match.Models[i] = strings.TrimSpace(m)
	}
	routes, err := s.Store.Routes(r.Context(), t)
	if err != nil {
		return in, nil, nil, err
	}
	backends, err := s.Store.Backends(r.Context(), t)
	if err != nil {
		return in, nil, nil, err
	}
	if err := routing.ValidateRoute(in, routes, backends); err != nil {
		return in, nil, nil, badRequest(err.Error())
	}
	return in, routes, backends, nil
}

// RouteDryRun is the rule a route write would compile to, without writing.
type RouteDryRun struct {
	DryRun bool        `json:"dryRun"`
	Route  model.Route `json:"route"`
	YAML   string      `json:"yaml"`
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	in, _, backends, err := s.readRoute(r, t, "")
	if err != nil {
		return nil, err
	}
	in.CaptureContent = false // not editable yet: it compiles to nothing in the gateway
	if dryRun(r) {
		return RouteDryRun{true, in, routing.RuleYAML(in, backends)}, nil
	}
	if _, err := s.Store.CreateRoute(r.Context(), t, actor(r), in); err != nil {
		return nil, err
	}
	s.configChanged()
	return s.routeView(w, r.Context(), t, in.Name)
}

// updateRoute replaces a route's match, targets and fallback. Its name is
// fixed: a route under another name is a new route.
func (s *Server) updateRoute(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	match := r.Header.Get("If-Match")
	if !dryRun(r) {
		var err error
		if match, err = ifMatch(r); err != nil {
			return nil, err
		}
	}
	in, routes, backends, err := s.readRoute(r, t, r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(routes, func(x model.Route) bool { return x.Name == in.Name })
	if i < 0 {
		return nil, store.ErrNotFound
	}
	in.CaptureContent = routes[i].CaptureContent
	if dryRun(r) {
		if match != "" && match != routes[i].ETag {
			return nil, &store.StaleError{Current: routes[i]}
		}
		return RouteDryRun{true, in, routing.RuleYAML(in, backends)}, nil
	}
	if _, err := s.Store.UpdateRoute(r.Context(), t, actor(r), match, in); err != nil {
		return nil, err
	}
	s.configChanged()
	return s.routeView(w, r.Context(), t, in.Name)
}

func (s *Server) deleteRoute(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	name := r.PathValue("name")
	if err := s.Store.DeleteRoute(r.Context(), t, actor(r), name, m); err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]string{"name": name}, nil
}

func (s *Server) plan(ctx context.Context, t string) (RoutingPlan, error) {
	p := RoutingPlan{Changes: []routing.Change{}}
	rs, err := s.Store.Routes(ctx, t)
	if err != nil {
		return p, err
	}
	bs, err := s.Store.Backends(ctx, t)
	if err != nil {
		return p, err
	}
	p.desired = routing.Compile(bs, rs)
	var yamls []string
	for _, o := range p.desired {
		yamls = append(yamls, o.YAML())
	}
	p.YAML = strings.Join(yamls, "---\n")
	if s.Routing == nil {
		p.Reason = "No gateway to apply to is configured (stargate-api serve -aigw-config)."
		p.ETag = store.ETag(p.YAML)
		return p, nil
	}
	p.Target = s.Routing.Target()
	if err := s.Routing.CanApply(); err != nil {
		p.Reason = err.Error()
	} else {
		p.CanApply = true
	}
	running, err := s.Routing.Running(ctx)
	if err != nil {
		return p, err
	}
	if cs := routing.Diff(running, p.desired); cs != nil {
		p.Changes = cs
	}
	var have []string
	for _, o := range running {
		have = append(have, o.YAML())
	}
	p.ETag = store.ETag([]string{p.YAML, strings.Join(have, "---\n")})
	p.LastApply, err = s.Store.LastApply(ctx, t)
	return p, err
}

func (s *Server) routingPlan(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	p, err := s.plan(r.Context(), t)
	if err == nil {
		w.Header().Set("ETag", p.ETag)
	}
	return p, err
}

// ApplyResult is an apply that went through.
type ApplyResult struct {
	OK      bool             `json:"ok"`
	Changes []routing.Change `json:"changes"`
}

// applyRouting puts the desired routing in front of the gateway. If-Match is
// the plan's etag: if routes or the running config moved since the plan was
// reviewed, it's refused with the current plan. A gateway that doesn't take
// the new config is rolled back, and its error comes back verbatim (502).
func (s *Server) applyRouting(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	// The apply and its rollback finish even if the caller goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyTimeout)
	defer cancel()
	p, err := s.plan(ctx, t)
	if err != nil {
		return nil, err
	}
	if m != p.ETag {
		return nil, &store.StaleError{Current: p}
	}
	if !p.CanApply {
		return nil, unavailable(p.Reason)
	}
	if len(p.Changes) == 0 {
		return ApplyResult{OK: true, Changes: p.Changes}, nil
	}
	running, err := s.Routing.Running(ctx)
	if err != nil {
		return nil, err
	}
	rs, err := s.Store.Routes(ctx, t)
	if err != nil {
		return nil, err
	}
	bs, err := s.Store.Backends(ctx, t)
	if err != nil {
		return nil, err
	}
	tries := attempted(running, rs, bs)
	applyErr := s.Routing.Apply(ctx, p.desired)
	msg := ""
	if applyErr != nil {
		msg = applyErr.Error()
	}
	if err := s.Store.RecordApply(ctx, t, actor(r), applyErr == nil, msg, p.Changes, tries); err != nil {
		return nil, err
	}
	if applyErr != nil {
		return nil, applyFailed(msg)
	}
	return ApplyResult{OK: true, Changes: p.Changes}, nil
}
