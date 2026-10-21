package openmeter

import (
	"net/http"
	"strconv"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/api"
)

// receiptCount counts requests in [since, before) from the llm_requests
// meter. With Traffic's filters it says it can't count exactly, as
// Stargate's does when its aggregate can't.
func (s *Server) receiptCount(r *http.Request) (any, error) {
	q := r.URL.Query()
	ms := func(name string) int64 {
		v, _ := strconv.ParseInt(q.Get(name), 10, 64)
		return v
	}
	now := s.Now().UTC()
	from, to := now.Add(-catalogSpan), now
	if v := ms("since"); v > 0 {
		from = time.UnixMilli(v)
	}
	if v := ms("before"); v > 0 {
		to = time.UnixMilli(v)
	}
	out := api.ReceiptCount{Since: from.UnixMilli()}
	for _, f := range []string{"key", "team", "project", "model", "verdict", "provider", "backend", "reason", "session"} {
		if q.Has(f) {
			out.Reason = "Counts aren't available with filters."
			return out, nil
		}
	}
	rows, err := s.Client.meterQuery(r.Context(), "llm_requests", from, to, nil, nil)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, row := range rows {
		n += int(row.Value)
	}
	out.Count = &n
	return out, nil
}

// activity is configuration history and traffic events; this pack records
// neither.
func (s *Server) activity(r *http.Request) (any, error) {
	_, span := rangeOf(r)
	now := s.Now().UTC()
	return api.ActivityView{Since: now.Add(-span).UnixMilli(), Until: now.UnixMilli(), Changes: []api.ActivityChange{}, Events: []api.TrafficEvent{}}, nil
}

// savings would suggest cheaper models through aliases, which this pack
// doesn't route.
func (s *Server) savings(*http.Request) (any, error) {
	now := s.Now().UTC()
	return api.SavingsView{From: now.Add(-catalogSpan).UnixMilli(), To: now.UnixMilli(), Opportunities: []api.SavingsOpportunity{}}, nil
}

// gatewayOverhead is the gateway's own time per request, which the usage
// events don't carry.
func (s *Server) gatewayOverhead(*http.Request) (any, error) {
	return map[string]any{"p50Ms": nil, "p95Ms": nil, "samples": 0, "windowMinutes": 15, "goalMs": 0}, nil
}
