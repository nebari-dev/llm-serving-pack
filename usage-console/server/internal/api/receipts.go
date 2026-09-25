package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jbouder/stargate/server/internal/store"
)

// receiptQuery reads the Traffic filters: limit, before and since (epoch ms),
// range (15m…30d, which sets since to the start of its first 5-minute bucket),
// and key, team, project, model, verdict, provider, backend, reason and
// session, each repeatable.
func receiptQuery(r *http.Request) store.ReceiptQuery {
	q := r.URL.Query()
	ms := func(name string) int64 {
		v, _ := strconv.ParseInt(q.Get(name), 10, 64)
		return v
	}
	rq := store.ReceiptQuery{
		Limit:     intParam(r, "limit", 240, 1, 1000),
		Before:    ms("before"),
		Since:     ms("since"),
		Keys:      q["key"],
		Teams:     q["team"],
		Projects:  q["project"],
		Models:    q["model"],
		Verdicts:  q["verdict"],
		Providers: q["provider"],
		Backends:  q["backend"],
		Reasons:   q["reason"],
		Sessions:  q["session"],
	}
	if q.Has("range") {
		_, d := rangeDuration(r)
		from := time.Now().Add(-d).Truncate(5 * time.Minute).UnixMilli()
		rq.Since = max(rq.Since, from)
	}
	return rq
}

func (s *Server) receipts(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.ListReceipts(r.Context(), t, receiptQuery(r))
}

// ReceiptCount is how many settled receipts match, from receipts_5m. Count is
// null when the aggregate can't answer exactly, and Reason says why.
type ReceiptCount struct {
	Count  *int   `json:"count"`
	Since  int64  `json:"since"`
	Reason string `json:"reason,omitempty"`
}

// receiptCount is GET /receipts/count with the same filters as /receipts;
// before, if given, ends the window.
func (s *Server) receiptCount(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	q := receiptQuery(r)
	out := ReceiptCount{Since: q.Since}
	switch {
	case q.Since == 0:
		return nil, badRequest("count needs since or range")
	case q.Since%(5*60_000) != 0 || q.Before%(5*60_000) != 0:
		out.Reason = "the window doesn't start and end on 5-minute boundaries"
	case !q.Aggregable():
		out.Reason = "project, model, provider, route reason and session aren't in the 5-minute aggregate"
	default:
		n, err := s.Store.CountReceipts(r.Context(), t, q)
		if err != nil {
			return nil, err
		}
		out.Count = &n
	}
	return out, nil
}
