package openmeter

import (
	"net/http"
	"strconv"
	"time"
)

// receiptCount counts requests in [since, before) from the llm_requests
// meter. With Traffic's filters it says it can't count exactly.
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
	out := ReceiptCount{Since: from.UnixMilli()}
	for _, f := range []string{"key", "team", "project", "model", "verdict", "session"} {
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
