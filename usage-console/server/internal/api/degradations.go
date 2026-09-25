package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Degradation is one condition for the console's banner slot (§7.4, §7.6):
// the request path is running in a state someone should know about.
type Degradation struct {
	Kind     string `json:"kind"`
	Severity int    `json:"severity"` // higher is worse; the banner shows the worst
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	To       string `json:"to"`
	Action   string `json:"action"`
	Since    int64  `json:"since,omitempty"` // epoch ms
}

const (
	degradationWindow = 15 * time.Minute
	staleSnapshot     = time.Minute
	// A backend is flagged once enough of its recent requests failed.
	backendMinRequests = 20
	backendFailShare   = 0.05
)

// wardenHealth is Warden's GET /healthz.
type wardenHealth struct {
	SnapshotAgeSeconds float64 `json:"snapshotAgeSeconds"`
	Passthrough        bool    `json:"passthrough"`
	Version            string  `json:"version"`
}

func (s *Server) fetchWarden(ctx context.Context) (*wardenHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(s.WardenURL, "/")+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("healthz returned %d", res.StatusCode)
	}
	var h wardenHealth
	return &h, json.NewDecoder(res.Body).Decode(&h)
}

// degradations reports what's degraded now, worst first: Warden's own state
// (when this server knows where Warden is), then what recent receipts show.
func (s *Server) degradations(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	out := []Degradation{}
	passthroughNow := false
	if s.WardenURL != "" {
		switch h, err := s.fetchWarden(ctx); {
		case err != nil:
			out = append(out, Degradation{Kind: "warden_unreachable", Severity: 3, To: "/guardrails", Action: "Review policies",
				Title:  "Warden isn't answering.",
				Detail: "Requests through Agent Router fail closed until it's back, and get no receipt."})
		case h.Passthrough:
			passthroughNow = true
			out = append(out, Degradation{Kind: "warden_passthrough", Severity: 3, To: "/guardrails", Action: "Review policies",
				Title:  "Warden's kill switch is on.",
				Detail: "Requests pass without budgets or rules. Each receipt is marked as not evaluated."})
		case h.SnapshotAgeSeconds > staleSnapshot.Seconds():
			out = append(out, Degradation{Kind: "warden_stale_config", Severity: 2, To: "/guardrails", Action: "Review policies",
				Title:  "Warden is enforcing cached config.",
				Detail: fmt.Sprintf("It can't reload from the control plane. Cache age %s; changes since then aren't applied.", age(h.SnapshotAgeSeconds))})
		}
	}

	since := time.Now().Add(-degradationWindow)
	modes, err := s.Store.RecentPolicyModes(ctx, t, since)
	if err != nil {
		return nil, err
	}
	for _, m := range modes {
		d := Degradation{Severity: 2, To: "/guardrails", Action: "Review policies", Since: m.Since.UnixMilli()}
		switch m.Mode {
		case "fail-open":
			d.Kind, d.Title = "policy_fail_open", "Policy is running fail-open."
			d.Detail = fmt.Sprintf("%s in the last %d minutes passed without rules: Warden couldn't decide in time and every enforced rule fails open.", requests(m.Count), int(degradationWindow.Minutes()))
		case "fail-closed":
			d.Kind, d.Title = "policy_fail_closed", "Policy is failing closed."
			d.Detail = fmt.Sprintf("%s in the last %d minutes were refused with 503 because Warden couldn't evaluate them in time.", requests(m.Count), int(degradationWindow.Minutes()))
		case "passthrough":
			if passthroughNow {
				continue
			}
			d.Kind, d.Title = "policy_passthrough", "Warden's kill switch was on."
			d.Detail = fmt.Sprintf("%s in the last %d minutes passed without budgets or rules.", requests(m.Count), int(degradationWindow.Minutes()))
		default:
			continue
		}
		out = append(out, d)
	}

	backends, err := s.Store.RecentBackendFailures(ctx, t, since)
	if err != nil {
		return nil, err
	}
	for _, b := range backends {
		if b.Total < backendMinRequests || float64(b.Failed) < backendFailShare*float64(b.Total) {
			continue
		}
		out = append(out, Degradation{Kind: "backend_errors", Severity: 1, To: "/routing", Action: "View backend",
			Title: b.Backend + " is failing requests.",
			Detail: fmt.Sprintf("%d%% of its %d requests in the last %d minutes failed, most often with %d.",
				100*b.Failed/b.Total, b.Total, int(degradationWindow.Minutes()), b.TopStatus)})
	}
	slices.SortStableFunc(out, func(a, b Degradation) int { return b.Severity - a.Severity })
	return out, nil
}

func requests(n int) string {
	if n == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", n)
}

// age renders seconds the way the banner does: "4m 12s".
func age(sec float64) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
