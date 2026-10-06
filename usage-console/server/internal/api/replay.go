package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Replay (spec §7.5.7): a policy's draft run over recorded requests with the
// evaluator Warden uses, against the policies as they are. Requests whose
// route captured content replay exactly; the rest on metadata only.

// ReplayLimit is the most requests one replay reads, newest first (§10:
// 10,000 within 5 seconds).
const ReplayLimit = 10_000

var replayWindows = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

type ReplayResponse struct {
	gateway.ReplayResult
	Window string `json:"window"`
	Since  int64  `json:"since"` // epoch ms
	Until  int64  `json:"until"`
	// Limited: the window held more than Limit requests; the newest were replayed.
	Limited bool `json:"limited"`
	Limit   int  `json:"limit"`
	// Replayed is which rules ran: "unsaved" (sent with the request), "draft"
	// (the saved draft) or "live" (no draft: the live version, as enforced).
	Replayed string `json:"replayed"`
	// CaptureRoutes capture content now. Content exists only for requests
	// made on them while capture was on.
	CaptureRoutes []string `json:"captureRoutes"`
	MS            int64    `json:"ms"`
}

// replayPolicy is POST /policies/{id}/replay?window=1h. A body (as for a
// draft save) replays those rules unsaved; without one, the saved draft, or
// the live version.
func (s *Server) replayPolicy(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	id := r.PathValue("id")
	ps, err := s.Store.Policies(ctx, t)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(ps, func(p model.Policy) bool { return p.ID == id })
	if i < 0 {
		return nil, store.ErrNotFound
	}
	draft, replayed := ps[i], "live"
	if r.ContentLength != 0 {
		c, err := s.policyContent(r, t)
		if err != nil {
			return nil, err
		}
		draft.Rules, replayed = c.Rules, "unsaved"
	} else {
		drafts, err := s.Store.PolicyDrafts(ctx, t)
		if err != nil {
			return nil, err
		}
		if d := drafts[id]; d != nil {
			draft.Rules, replayed = d.Rules, "draft"
		}
	}
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "1h"
	}
	if _, ok := replayWindows[window]; !ok {
		return nil, badRequest("window must be 1h, 24h, 7d or 30d")
	}
	res, err := s.replay(ctx, t, draft, window)
	if err != nil {
		return nil, err
	}
	res.Replayed = replayed
	return res, nil
}

// replay runs draft over the window's requests.
func (s *Server) replay(ctx context.Context, t string, draft model.Policy, window string) (*ReplayResponse, error) {
	start := time.Now()
	snap, err := gateway.LoadSnapshot(ctx, s.Store, t)
	if err != nil {
		return nil, err
	}
	rp := gateway.NewReplay(snap, draft)
	since := start.Add(-replayWindows[window])
	n, err := s.Store.ReplayRows(ctx, t, since, ReplayLimit, func(row store.ReplayRow) {
		it := gateway.ReplayItem{ID: row.ID, TS: row.TS, KeyID: row.KeyID, KeyName: row.KeyName, Team: row.Team,
			ProjectID: row.ProjectID, Model: row.Model, Region: row.Region}
		if row.Content != nil {
			var c struct {
				Messages []fakellm.Message `json:"messages"`
			}
			if json.Unmarshal(row.Content, &c) == nil && c.Messages != nil {
				it.Messages = c.Messages
			}
		}
		rp.Add(it)
	})
	if err != nil {
		return nil, err
	}
	out := &ReplayResponse{ReplayResult: rp.Result(), Window: window, Since: since.UnixMilli(), Until: start.UnixMilli(),
		Limited: n == ReplayLimit, Limit: ReplayLimit, CaptureRoutes: []string{}}
	for _, rt := range snap.Routes {
		if rt.CaptureContent {
			out.CaptureRoutes = append(out.CaptureRoutes, rt.Name)
		}
	}
	out.MS = time.Since(start).Milliseconds()
	return out, nil
}

// setRouteCapture is PUT /routes/{name}/capture {captureContent}: §9.2's
// per-route opt-in, an elevated role's action with its own audit row. It
// takes effect at Warden's next reload, without an apply: the gateway's
// config doesn't change.
func (s *Server) setRouteCapture(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		CaptureContent *bool `json:"captureContent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.CaptureContent == nil {
		return nil, badRequest("body must be {\"captureContent\": true|false}")
	}
	rt, err := s.Store.SetRouteCapture(r.Context(), t, actor(r), r.PathValue("name"), m, *in.CaptureContent)
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.routeView(w, r.Context(), t, rt.Name)
}
