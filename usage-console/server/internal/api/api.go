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
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

type Server struct {
	Store *store.Store
	Hub   *Hub
	// Tenants this server answers for. Auth (OIDC, §6) is not wired yet, so
	// every caller acts as DevActor.
	Tenants  []string
	DevActor string
	// KeysChanged, if set, runs after a key is created, revoked or rotated and
	// before the response, so the gateway's key check sees it at once.
	KeysChanged func()
	// WardenURL is Warden's admin base URL (http://localhost:8084), for the
	// degradation banner. Empty when Warden isn't in the request path.
	WardenURL string
	// Environment names the deployment this control plane serves, shown in
	// the console header ("production" gets the production accent).
	Environment string
}

func (s *Server) keysChanged() {
	if s.KeysChanged != nil {
		s.KeysChanged()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn func(http.ResponseWriter, *http.Request, string) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			tenant := r.PathValue("tenant")
			if !slices.Contains(s.Tenants, tenant) {
				writeJSON(w, 404, errBody("tenant_not_found", "unknown tenant "+tenant))
				return
			}
			v, err := fn(w, r, tenant)
			switch {
			case errors.Is(err, store.ErrNotFound):
				writeJSON(w, 404, errBody("not_found", "not found"))
			case errors.Is(err, store.ErrConflict):
				writeJSON(w, 409, errBody("conflict", err.Error()))
			case errors.As(err, new(badRequest)):
				writeJSON(w, 400, errBody("bad_request", err.Error()))
			case errors.As(err, new(unavailable)):
				writeJSON(w, 503, errBody("unavailable", err.Error()))
			case err != nil:
				log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
				writeJSON(w, 500, errBody("internal", "internal error"))
			case v != nil:
				writeJSON(w, http.StatusOK, v)
			}
		})
	}
	const p = "/api/v1/{tenant}"
	h("GET "+p+"/teams", s.teams)
	h("GET "+p+"/models", s.models)
	h("GET "+p+"/aliases", s.aliases)
	h("GET "+p+"/pricing", s.pricing)
	h("GET "+p+"/backends", s.backends)
	h("GET "+p+"/routes", s.routes)
	h("GET "+p+"/keys", s.keys)
	h("POST "+p+"/keys", s.createKey)
	h("POST "+p+"/keys/{id}/revoke", s.revokeKey)
	h("POST "+p+"/keys/{id}/rotate", s.rotateKey)
	h("GET "+p+"/budgets", s.budgets)
	h("GET "+p+"/rules", s.rules)
	h("GET "+p+"/detectors", s.detectors)
	h("GET "+p+"/changes", s.changes)
	h("GET "+p+"/receipts", s.receipts)
	h("GET "+p+"/receipts/count", s.receiptCount)
	h("GET "+p+"/receipts/{id}", s.receipt)
	h("GET "+p+"/series/traffic", s.trafficSeries)
	h("GET "+p+"/series/spend", s.spendSeries)
	h("GET "+p+"/spend", s.spend)
	h("GET "+p+"/stream/traffic", s.streamTraffic)
	h("GET "+p+"/degradations", s.degradations)
	h("GET "+p+"/session", s.session)
	h("GET "+p+"/summary", s.summary)
	h("GET "+p+"/changes/{id}/impact", s.changeImpact)
	h("GET "+p+"/activity", s.activity)
	h("GET "+p+"/retention", s.retention)
	h("POST "+p+"/warden/passthrough", s.setPassthrough)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

// unavailable is a dependency this request needs (Warden) being unreachable.
type unavailable string

func (u unavailable) Error() string { return string(u) }

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
	return s.Store.Models(r.Context())
}

func (s *Server) routes(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Routes(r.Context(), t)
}

func (s *Server) detectors(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Detectors(r.Context(), t)
}

func (s *Server) changes(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Changes(r.Context(), t, intParam(r, "limit", 50, 1, 500))
}

// backends overlays live p50 and error rate from the last hour of receipts.
// Health stays as configured until health probes exist.
func (s *Server) backends(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	bs, err := s.Store.Backends(r.Context(), t)
	if err != nil {
		return nil, err
	}
	stats, err := s.Store.BackendStats(r.Context(), t)
	if err != nil {
		return nil, err
	}
	for i := range bs {
		if st, ok := stats[bs[i].Name]; ok && st.Requests >= 5 {
			bs[i].P50, bs[i].ErrorRate = st.P50, math.Round(st.ErrorRate*10)/10
		}
	}
	return bs, nil
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
	out := make([]model.APIKey, len(ks))
	for i, k := range ks {
		out[i] = withUsage(k.APIKey, usage[k.ID])
		out[i].Rotation = rotationOf(k, starts)
	}
	return out, nil
}

func withUsage(k model.APIKey, u store.KeyUsage) model.APIKey {
	if u.LastUsed != nil {
		ms := u.LastUsed.UnixMilli()
		k.LastUsedAt = &ms
	}
	if k.Status == "revoked" {
		u = store.KeyUsage{}
	}
	k.Requests24h, k.Spend24hUSD, k.Hourly24h = u.Requests24h, u.Spend24hUSD, u.Hourly[:]
	return k
}

// rotationOf is a rotating key's window: its end from rotate_until, and its
// start from the key's latest "Rotated key" audit row.
func rotationOf(k store.KeyRecord, starts map[string]store.AuditMark) *model.KeyRotation {
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
	case in.Name == "" || in.Team == "" || in.Project == "":
		return nil, badRequest("name, team and project are required")
	case len(in.AllowedModels) == 0:
		return nil, badRequest("at least one allowed model is required")
	}
	k, secret, err := s.Store.CreateKey(r.Context(), t, s.DevActor, in)
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23503" {
		return nil, badRequest("unknown team or budget")
	}
	if err != nil {
		return nil, err
	}
	s.keysChanged()
	return map[string]any{"key": k.APIKey, "secret": secret}, nil
}

func (s *Server) revokeKey(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	k, err := s.Store.RevokeKey(r.Context(), t, s.DevActor, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	s.keysChanged()
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
	k, secret, err := s.Store.RotateKey(r.Context(), t, s.DevActor, r.PathValue("id"), time.Duration(in.OverlapHours)*time.Hour)
	if err != nil {
		return nil, err
	}
	s.keysChanged()
	usage, _ := s.Store.KeyUsage(r.Context(), t)
	starts, _ := s.Store.RotationStarts(r.Context(), t)
	out := withUsage(k.APIKey, usage[k.ID])
	out.Rotation = rotationOf(k, starts)
	return map[string]any{"key": out, "secret": secret}, nil
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
	for i := range rs {
		c := counts[rs[i].ID]
		rs[i].Fired24h, rs[i].Baseline7d = c.Last24h, int(math.Round(float64(c.Last7d)/7))
	}
	return rs, nil
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
