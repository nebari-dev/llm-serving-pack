// Package api is the control-plane REST + SSE surface the console reads
// (spec §6), scoped as /api/v1/{tenant}/...
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jbouder/stargate/server/internal/auth"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/receiptsig"
	"github.com/jbouder/stargate/server/internal/routing"
	"github.com/jbouder/stargate/server/internal/store"
)

type Server struct {
	Store *store.Store
	Hub   *Hub
	// Tenants this server answers for.
	Tenants []string
	// Auth signs people in (OIDC against Keycloak). Nil is dev mode: every
	// caller is Dev, with no credentials.
	Auth *auth.OIDC
	// Dev is who every caller is in dev mode; empty is DevUser
	// (dev@localhost, owner).
	Dev auth.User
	// ConfigChanged, if set, runs after a write to anything the gateway
	// enforces (keys, aliases, budgets, rules, prices) and before the
	// response, so the key check sees it at once. Warden is asked to reload
	// too; if it can't be reached it catches up on its next tick.
	ConfigChanged func()
	// WardenURL is Warden's admin base URL (http://localhost:8084), for the
	// degradation banner. Empty when Warden isn't in the request path.
	WardenURL string
	// Environment names the deployment this control plane serves, shown in
	// the console header ("production" gets the production accent).
	Environment string
	// LiteLLMURL is where the price sync reads LiteLLM's file; empty is
	// LiteLLM's GitHub copy.
	LiteLLMURL string
	// GatewayURL is the gateway callers use (http://localhost:1975), shown on
	// onboarding and used for its test request. Empty when unknown.
	GatewayURL string
	// Routing puts the desired routing in front of the gateway; nil when
	// there's no gateway to apply to (routes are then not_reconciled).
	Routing routing.Applier
	// Keys stores provider keys where the gateway reads them; nil when there's
	// nowhere to (keys can't be set, and tests of a keyed backend can't run).
	Keys routing.KeyStore
	// Signer signs receipt exports; nil when there's no key (exports are
	// then refused, 503).
	Signer  *receiptsig.Signer
	syncMu  sync.Mutex
	applyMu sync.Mutex
	// seen is the members cache's last write per user (touchUser).
	seen sync.Map
	// keyOwner replaces Store.KeyOwner in tests.
	keyOwner func(ctx context.Context, tenant, id string) (string, error)
	// patterns is every route Handler registered, for tests.
	patterns []string
}

func (s *Server) configChanged() {
	if s.ConfigChanged != nil {
		s.ConfigChanged()
	}
	if s.WardenURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(s.WardenURL, "/")+"/reload", nil)
	if err != nil {
		return
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("warden reload: %v (it reloads on its next tick)", err)
		return
	}
	res.Body.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.patterns = nil
	h := func(pattern string, fn handlerFunc) {
		s.patterns = append(s.patterns, pattern)
		mux.HandleFunc(pattern, s.wrap(pattern, fn))
	}
	if s.Auth != nil {
		mux.HandleFunc("GET /api/auth/login", s.Auth.Login)
		mux.HandleFunc("GET "+auth.CallbackPath, s.Auth.Callback)
		mux.HandleFunc("POST /api/auth/logout", s.Auth.Logout)
	}
	const p = "/api/v1/{tenant}"
	h("GET "+p+"/teams", s.teams)
	h("GET "+p+"/members", s.members)
	h("GET "+p+"/projects", s.projects)
	h("POST "+p+"/projects", s.createProject)
	h("PUT "+p+"/projects/{id}", s.renameProject)
	h("DELETE "+p+"/projects/{id}", s.deleteProject)
	h("GET "+p+"/models", s.models)
	h("GET "+p+"/aliases", s.aliases)
	h("PUT "+p+"/aliases/{alias}", s.putAlias)
	h("DELETE "+p+"/aliases/{alias}", s.deleteAlias)
	h("GET "+p+"/pricing", s.pricing)
	h("POST "+p+"/pricing/sync", s.syncNow)
	h("POST "+p+"/pricing/proposals/{id}/accept", s.acceptProposal)
	h("POST "+p+"/pricing/proposals/{id}/dismiss", s.dismissProposal)
	h("POST "+p+"/pricing/{model}/{backend}", s.setPrice)
	h("PUT "+p+"/pricing/{model}/{backend}/source", s.setPriceSource)
	h("DELETE "+p+"/pricing/{model}/{backend}/{at}", s.cancelPrice)
	h("GET "+p+"/backends", s.backends)
	h("POST "+p+"/backends", s.createBackend)
	h("POST "+p+"/backends/test", s.testProvider)
	h("PUT "+p+"/backends/{name}", s.updateBackend)
	h("DELETE "+p+"/backends/{name}", s.deleteBackend)
	h("PUT "+p+"/backends/{name}/key", s.setBackendKey)
	h("POST "+p+"/backends/{name}/test", s.testBackend)
	h("GET "+p+"/routes", s.routes)
	h("POST "+p+"/routes", s.createRoute)
	h("PUT "+p+"/routes/{name}", s.updateRoute)
	h("DELETE "+p+"/routes/{name}", s.deleteRoute)
	h("GET "+p+"/routing", s.routingPlan)
	h("POST "+p+"/routing/apply", s.applyRouting)
	h("GET "+p+"/keys", s.keys)
	h("POST "+p+"/keys", s.createKey)
	h("POST "+p+"/keys/{id}/revoke", s.revokeKey)
	h("POST "+p+"/keys/{id}/rotate", s.rotateKey)
	h("POST "+p+"/keys/{id}/rotation/extend", s.extendRotation)
	h("POST "+p+"/keys/{id}/rotation/finish", s.finishRotation)
	h("GET "+p+"/budgets", s.budgets)
	h("POST "+p+"/budgets", s.createBudget)
	h("PATCH "+p+"/budgets/{id}", s.updateBudget)
	h("DELETE "+p+"/budgets/{id}", s.deleteBudget)
	h("GET "+p+"/rules", s.rules)
	h("GET "+p+"/rules/vocabulary", s.ruleVocabulary)
	h("POST "+p+"/rules", s.createRule)
	h("PUT "+p+"/rules/order", s.reorderRules)
	h("PUT "+p+"/rules/{id}/draft", s.saveRuleDraft)
	h("DELETE "+p+"/rules/{id}/draft", s.discardRuleDraft)
	h("POST "+p+"/rules/{id}/publish", s.publishRule)
	h("POST "+p+"/rules/{id}/rollback", s.rollbackRule)
	h("GET "+p+"/rules/{id}/versions", s.ruleVersions)
	h("DELETE "+p+"/rules/{id}", s.deleteRule)
	h("GET "+p+"/detectors", s.detectors)
	h("GET "+p+"/detectors/hits", s.detectorHits)
	h("POST "+p+"/detectors/hits/verdict", s.setVerdict)
	h("POST "+p+"/entities", s.createEntity)
	h("PUT "+p+"/entities/{id}", s.updateEntity)
	h("DELETE "+p+"/entities/{id}", s.deleteEntity)
	h("GET "+p+"/changes", s.changes)
	h("GET "+p+"/receipts", s.receipts)
	h("GET "+p+"/receipts/count", s.receiptCount)
	h("GET "+p+"/receipts/signing-key", s.signingKey)
	h("POST "+p+"/receipts/export", s.exportReceipts)
	h("POST "+p+"/receipts/{id}/export", s.exportReceipt)
	h("POST "+p+"/receipts/{id}/reveal", s.revealContent)
	h("GET "+p+"/receipts/{id}", s.receipt)
	h("GET "+p+"/series/traffic", s.trafficSeries)
	h("GET "+p+"/series/spend", s.spendSeries)
	h("GET "+p+"/spend", s.spend)
	h("GET "+p+"/spend/savings", s.savings)
	h("GET "+p+"/spend/close-report", s.closeReport)
	h("GET "+p+"/stream/traffic", s.streamTraffic)
	h("GET "+p+"/degradations", s.degradations)
	h("GET "+p+"/session", s.session)
	h("GET "+p+"/summary", s.summary)
	h("GET "+p+"/changes/{id}/impact", s.changeImpact)
	h("GET "+p+"/activity", s.activity)
	h("GET "+p+"/retention", s.retention)
	h("POST "+p+"/warden/passthrough", s.setPassthrough)
	h("POST "+p+"/gateway/test", s.gatewayTest)
	h("GET "+p+"/gateway/overhead", s.gatewayOverhead)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

type handlerFunc func(http.ResponseWriter, *http.Request, string) (any, error)

// wrap is every tenant route: the tenant, then who's asking (401 without a
// session in OIDC mode), then their roles against the route's action (403),
// then fn, its answer or error mapped to a status.
func (s *Server) wrap(pattern string, fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := r.PathValue("tenant")
		if !slices.Contains(s.Tenants, tenant) {
			writeJSON(w, 404, errBody("tenant_not_found", "unknown tenant "+tenant))
			return
		}
		u, err := s.authenticate(w, r)
		if err == nil {
			err = s.authorize(r, tenant, u, pattern)
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		s.touchUser(r.Context(), tenant, u)
		r = r.WithContext(withUser(r.Context(), u))
		v, err := fn(w, r, tenant)
		if err != nil {
			writeError(w, r, err)
		} else if v != nil {
			writeJSON(w, http.StatusOK, v)
		}
	}
}

// writeError answers a failed request with the status its error means.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	if writeAuthError(w, err) {
		return
	}
	var stale *store.StaleError
	switch {
	case errors.As(err, &stale):
		// §6: a stale write is a 409 carrying what's there now, for a merge.
		body := errBody("conflict", err.Error())
		body["current"] = stale.Current
		writeJSON(w, 409, body)
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, 404, errBody("not_found", "not found"))
	case errors.Is(err, store.ErrConflict):
		writeJSON(w, 409, errBody("conflict", err.Error()))
	case errors.As(err, new(preconditionRequired)):
		writeJSON(w, 428, errBody("precondition_required", err.Error()))
	case errors.As(err, new(conflict)):
		writeJSON(w, 409, errBody("conflict", err.Error()))
	case errors.As(err, new(badRequest)):
		writeJSON(w, 400, errBody("bad_request", err.Error()))
	case errors.As(err, new(unavailable)):
		writeJSON(w, 503, errBody("unavailable", err.Error()))
	case errors.As(err, new(applyFailed)):
		writeJSON(w, 502, errBody("apply_failed", err.Error()))
	case errors.As(err, new(keyRefused)):
		writeJSON(w, 422, errBody("key_test_failed", err.Error()))
	case err != nil:
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		writeJSON(w, 500, errBody("internal", "internal error"))
	}
}

// preconditionRequired is an update or delete without If-Match (428).
type preconditionRequired string

func (p preconditionRequired) Error() string { return string(p) }

// ifMatch is the version an update or delete says it's changing (§6). One
// that doesn't say is refused: it could overwrite a change it never saw.
func ifMatch(r *http.Request) (string, error) {
	if m := r.Header.Get("If-Match"); m != "" {
		return m, nil
	}
	return "", preconditionRequired("send If-Match with the etag of the version you're changing")
}

// conflict is a write the resource's current state doesn't allow (409).
type conflict string

func (c conflict) Error() string { return string(c) }

type badRequest string

func (b badRequest) Error() string { return string(b) }

// unavailable is a dependency this request needs (Warden) being unreachable.
type unavailable string

func (u unavailable) Error() string { return string(u) }

// applyFailed is a gateway that didn't take an apply (502), in its own words.
type applyFailed string

func (a applyFailed) Error() string { return string(a) }

// keyRefused is a provider key that failed its connection test, so nothing
// was saved (422): the provider's error, the key scrubbed from it.
type keyRefused string

func (k keyRefused) Error() string { return string(k) }

func errBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func intParam(r *http.Request, name string, def, lo, hi int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}

func (s *Server) teams(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Teams(r.Context(), t)
}

func (s *Server) models(_ http.ResponseWriter, r *http.Request, _ string) (any, error) {
	ms, err := s.Store.Models(r.Context())
	if err != nil {
		return nil, err
	}
	facts, err := s.Store.ModelFacts(r.Context())
	if err != nil {
		return nil, err
	}
	return withFacts(ms, facts), nil
}

// withFacts adds what LiteLLM says about each model on the backends serving
// it: the union of their modalities, in first-seen order, and any
// deprecation dates.
func withFacts(ms []model.Model, facts []store.PairFacts) []model.Model {
	out := slices.Clone(ms)
	for i := range out {
		for _, f := range facts {
			if f.Model != out[i].ID {
				continue
			}
			for _, m := range f.Modalities {
				if !slices.Contains(out[i].Modalities, m) {
					out[i].Modalities = append(out[i].Modalities, m)
				}
			}
			if f.Deprecation != "" {
				out[i].Deprecations = append(out[i].Deprecations, model.Deprecation{Backend: f.Backend, Date: f.Deprecation})
			}
		}
	}
	return out
}

func (s *Server) changes(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Changes(r.Context(), t, intParam(r, "limit", 50, 1, 500), r.URL.Query().Get("kind"))
}

// backends reports what receipts show, not what was seeded: health from the
// banner's window, p50 and error rate from the last hour. Sync is whether the
// gateway runs the backend as it is (withBackendSync).
func (s *Server) backends(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	bs, err := s.Store.Backends(r.Context(), t)
	if err != nil {
		return nil, err
	}
	stats, err := s.Store.BackendStats(r.Context(), t)
	if err != nil {
		return nil, err
	}
	recent, err := s.Store.RecentBackendFailures(r.Context(), t, time.Now().Add(-degradationWindow))
	if err != nil {
		return nil, err
	}
	for i := range bs {
		var f *store.BackendFailures
		for j := range recent {
			if recent[j].Backend == bs[i].Name {
				f = &recent[j]
			}
		}
		var h *store.BackendStats
		if st, ok := stats[bs[i].Name]; ok {
			h = &st
		}
		bs[i] = observedBackend(bs[i], f, h)
	}
	running, failed, ok, err := s.observed(r.Context(), t)
	if err != nil {
		return nil, err
	}
	if !ok {
		for i := range bs {
			bs[i].Sync = NotReconciled
		}
		return bs, nil
	}
	return withBackendSync(bs, running, failed), nil
}

// observedBackend replaces a backend's seeded health, p50 and error rate with
// observed ones. No requests in the window is "idle". Every request failing
// (at least backendMinFailed of them) is "down"; the banner's failing rule,
// or every one of fewer requests failing, is "degraded".
func observedBackend(b model.Backend, recent *store.BackendFailures, hour *store.BackendStats) model.Backend {
	b.Health, b.P50, b.ErrorRate, b.Requests1h = "idle", 0, 0, 0
	if hour != nil {
		b.P50, b.ErrorRate, b.Requests1h = hour.P50, math.Round(hour.ErrorRate*10)/10, hour.Requests
	}
	switch {
	case recent == nil || recent.Total == 0:
	case recent.Failed == recent.Total && recent.Failed >= backendMinFailed:
		b.Health = "down"
	case backendFailing(recent.Total, recent.Failed) || recent.Failed == recent.Total:
		b.Health = "degraded"
	default:
		b.Health = "healthy"
	}
	return b
}

func (s *Server) keys(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ks, err := s.Store.Keys(r.Context(), t)
	if err != nil {
		return nil, err
	}
	usage, err := s.Store.KeyUsage(r.Context(), t)
	if err != nil {
		return nil, err
	}
	starts, err := s.Store.RotationStarts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	bySecret, err := s.Store.RotationSecrets(r.Context(), t, rotatingSince(ks, starts))
	if err != nil {
		return nil, err
	}
	out := make([]model.APIKey, len(ks))
	for i, k := range ks {
		out[i] = withUsage(k.APIKey, usage[k.ID])
		out[i].Rotation = rotationOf(k, starts, bySecret)
	}
	return out, nil
}

// rotatingSince is when each rotating key's overlap started, where recorded.
func rotatingSince(ks []store.KeyRecord, starts map[string]store.AuditMark) map[string]time.Time {
	out := map[string]time.Time{}
	for _, k := range ks {
		if m, ok := starts[k.ID]; ok && k.Status == "rotating" {
			out[k.ID] = m.TS
		}
	}
	return out
}

func withUsage(k model.APIKey, u store.KeyUsage) model.APIKey {
	if u.LastUsed != nil {
		ms := u.LastUsed.UnixMilli()
		k.LastUsedAt = &ms
	}
	if k.Status == "revoked" {
		u = store.KeyUsage{}
	}
	k.Requests24h, k.Spend24hUSD, k.Unpriced24h, k.Hourly24h = u.Requests24h, u.Spend24hUSD, u.Unpriced24h, u.Hourly[:]
	return k
}

// rotationOf is a rotating key's window: its end from rotate_until, its
// start from the key's latest "Rotated key" audit row, and the requests since
// then by secret (bySecret: key id → secret id → requests).
func rotationOf(k store.KeyRecord, starts map[string]store.AuditMark, bySecret map[string]map[string]int) *model.KeyRotation {
	if k.Status != "rotating" {
		return nil
	}
	var r model.KeyRotation
	if k.RotateUntil != nil {
		ms := k.RotateUntil.UnixMilli()
		r.EndsAt = &ms
	}
	if m, ok := starts[k.ID]; ok {
		ms, actor := m.TS.UnixMilli(), m.Actor
		r.StartedAt, r.StartedBy = &ms, &actor
		counts := bySecret[k.ID]
		old, next := counts[gateway.SecretID(k.Hash)], counts[gateway.SecretID(k.NextHash)]
		r.OldSecretRequests, r.NewSecretRequests, r.UnrecordedRequests = &old, &next, counts[""]
	}
	return &r
}

func (s *Server) createKey(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in store.NewKey
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	in.Name, in.Project = strings.TrimSpace(in.Name), strings.TrimSpace(in.Project)
	switch {
	case in.Name == "" || in.Team == "" || in.Project == "" && in.ProjectID == "":
		return nil, badRequest("name, team and project are required")
	case len(in.AllowedModels) == 0:
		return nil, badRequest("at least one allowed model is required")
	}
	// The project must exist on the key's team: a key no longer creates one.
	if err := s.keyProject(r.Context(), t, &in); err != nil {
		return nil, err
	}
	k, secret, err := s.Store.CreateKey(r.Context(), t, actor(r), in)
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23503" {
		return nil, badRequest("unknown team")
	}
	if errors.Is(err, store.ErrNotFound) {
		// Deleted, or moved team, since keyProject looked.
		return nil, badRequest("Project " + in.Project + " isn't one of team " + in.Team + "'s projects any more.")
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]any{"key": k.APIKey, "secret": secret}, nil
}

func (s *Server) revokeKey(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	k, err := s.Store.RevokeKey(r.Context(), t, actor(r), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return withUsage(k.APIKey, store.KeyUsage{}), nil
}

// rotateKey takes {"overlapHours": n}: how long both secrets authenticate
// (1h to 7 days, default 48h, matching the console's rotate dialog).
func (s *Server) rotateKey(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		OverlapHours int `json:"overlapHours"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, badRequest("invalid JSON body")
		}
	}
	if in.OverlapHours == 0 {
		in.OverlapHours = 48
	}
	if in.OverlapHours < 1 || in.OverlapHours > 168 {
		return nil, badRequest("overlapHours must be between 1 and 168")
	}
	k, secret, err := s.Store.RotateKey(r.Context(), t, actor(r), r.PathValue("id"), time.Duration(in.OverlapHours)*time.Hour)
	if err != nil {
		return nil, err
	}
	s.configChanged()
	out, err := s.keyView(r.Context(), t, k)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": out, "secret": secret}, nil
}

// extendRotation takes {"hours": n}: how much longer both secrets work. The
// overlap can't end more than 7 days from now.
func (s *Server) extendRotation(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		Hours int `json:"hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	if in.Hours < 1 || in.Hours > 168 {
		return nil, badRequest("hours must be between 1 and 168")
	}
	k, err := s.Store.ExtendRotation(r.Context(), t, actor(r), r.PathValue("id"), time.Duration(in.Hours)*time.Hour)
	if errors.Is(err, store.ErrOverlapTooLong) {
		return nil, badRequest(err.Error())
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.keyView(r.Context(), t, k)
}

// finishRotation retires the old secret now, instead of at the overlap's end.
func (s *Server) finishRotation(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	k, err := s.Store.FinishRotation(r.Context(), t, actor(r), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.keyView(r.Context(), t, k)
}

// keyView is one key as GET /keys shows it.
func (s *Server) keyView(ctx context.Context, t string, k store.KeyRecord) (model.APIKey, error) {
	usage, err := s.Store.KeyUsage(ctx, t)
	if err != nil {
		return model.APIKey{}, err
	}
	starts, err := s.Store.RotationStarts(ctx, t)
	if err != nil {
		return model.APIKey{}, err
	}
	bySecret, err := s.Store.RotationSecrets(ctx, t, rotatingSince([]store.KeyRecord{k}, starts))
	if err != nil {
		return model.APIKey{}, err
	}
	out := withUsage(k.APIKey, usage[k.ID])
	out.Rotation = rotationOf(k, starts, bySecret)
	return out, nil
}

func (s *Server) rules(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	rs, err := s.Store.Rules(r.Context(), t)
	if err != nil {
		return nil, err
	}
	counts, err := s.Store.RuleCounts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	drafts, err := s.Store.RuleDrafts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	out := make([]store.RuleView, len(rs))
	for i := range rs {
		c := counts[rs[i].ID]
		rs[i].Fired24h, rs[i].Baseline7d = c.Last24h, int(math.Round(float64(c.Last7d)/7))
		out[i] = store.NewRuleView(rs[i], drafts[rs[i].ID])
	}
	return out, nil
}

func (s *Server) receipt(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Receipt(r.Context(), t, r.PathValue("id"), 0)
}

var rangeBuckets = map[string]struct {
	bucket time.Duration
	points int
}{
	"15m": {time.Minute * 5, 3},
	"1h":  {5 * time.Minute, 12},
	"6h":  {15 * time.Minute, 24},
	"24h": {30 * time.Minute, 48},
	"7d":  {6 * time.Hour, 28},
	"30d": {24 * time.Hour, 30},
}

func (s *Server) trafficSeries(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	rb, ok := rangeBuckets[r.URL.Query().Get("range")]
	if !ok {
		rb = rangeBuckets["24h"]
	}
	return s.Store.TrafficSeries(r.Context(), t, rb.bucket, rb.points)
}

func (s *Server) spendSeries(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	teams, err := s.Store.Teams(r.Context(), t)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(teams))
	for i, x := range teams {
		ids[i] = x.ID
	}
	return s.Store.SpendSeries(r.Context(), t, intParam(r, "days", 30, 1, 90), ids)
}

// FinishRotations runs until ctx ends, retiring secrets whose overlap is over.
func (s *Server) FinishRotations(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := s.Store.FinishRotations(ctx); err != nil && ctx.Err() == nil {
			log.Printf("finish rotations: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
