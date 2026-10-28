package openmeter

import (
	"net/http"
	"strconv"
	"time"
)

// receiptCount counts the requests Traffic's filters match in [since,
// before) from the llm_requests meter, which groups by every field they
// compare.
func (s *Server) receiptCount(r *http.Request) (any, error) {
	f := filterOf(r.URL.Query())
	now := s.Now().UTC()
	from, to := now.Add(-catalogSpan), now
	if f.since > 0 {
		from = time.UnixMilli(f.since)
	}
	if f.before > 0 {
		to = time.UnixMilli(f.before)
	}
	rows, err := s.Client.meterQuery(r.Context(), "llm_requests", from, to, nil,
		[]string{"api_key", "team", "model", "model_resource", "response_code", "usage_reported"})
	if err != nil {
		return nil, err
	}
	n := 0
	for _, row := range rows {
		g := func(k string) string {
			if v := row.GroupBy[k]; v != nil {
				return *v
			}
			return ""
		}
		status, _ := strconv.Atoi(g("response_code"))
		if f.match(fields{
			key: g("api_key"), team: g("team"), project: projectID(g("team"), g("model_resource")),
			model: g("model"), verdict: verdictOf(status, g("usage_reported")),
		}) {
			n += int(row.Value)
		}
	}
	return ReceiptCount{Count: n, Since: from.UnixMilli()}, nil
}
