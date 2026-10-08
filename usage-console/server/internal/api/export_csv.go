package api

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// receiptsCSV is settled receipts as a spreadsheet: one row each, newest
// first as given, and how many rows. A request still streaming has no usage
// yet and is left out. No price is "no price", never 0.
func receiptsCSV(rs []model.Receipt) ([]byte, int) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write([]string{"time_utc", "receipt_id", "key", "team", "project", "requested_model", "model", "backend", "provider", "route_reason",
		"verdict", "status", "error", "input_tokens", "output_tokens", "cost_usd", "duration_ms"})
	n := 0
	for _, r := range rs {
		if r.InFlight {
			continue
		}
		cost := "no price"
		if r.CostUSD != nil {
			cost = strconv.FormatFloat(*r.CostUSD, 'f', -1, 64)
		}
		w.Write([]string{time.UnixMilli(r.TS).UTC().Format("2006-01-02T15:04:05.000Z"), r.ID, r.KeyName, r.Team, r.Project, r.RequestedModel, r.ResolvedModel,
			r.Backend, r.Provider, r.RouteReason, r.Verdict, strconv.Itoa(r.Status), r.ErrorCode, strconv.Itoa(r.InputTokens), strconv.Itoa(r.OutputTokens), cost,
			strconv.Itoa(r.DurationMS)})
		n++
	}
	w.Flush()
	return b.Bytes(), n
}

// exportReceiptsCSV is POST /receipts/export.csv with GET /receipts' filters:
// the same receipts as the signed export, as a spreadsheet to read rather
// than evidence (no signature). Audited the same way, before any byte goes.
func (s *Server) exportReceiptsCSV(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	q := receiptQuery(r)
	q.Limit = exportCap + 1
	rs, err := s.Store.ListReceipts(r.Context(), t, q)
	if err != nil {
		return nil, err
	}
	if len(rs) > exportCap {
		return nil, badRequest(fmt.Sprintf("More than %d receipts match: narrow the time range or filters.", exportCap))
	}
	b, n := receiptsCSV(rs)
	target := fmt.Sprintf("%d receipts", n)
	if n == 1 {
		target = "1 receipt"
	}
	if err := s.Store.Audited(r.Context(), t, actor(r), "Exported receipts as CSV", target, store.AccessKind, nil,
		map[string]any{"filter": filterOf(q), "count": n}, func() error { return nil }); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("receipts-%s-%s.csv", t, now.Format("20060102T150405Z"))))
	w.Header().Set("X-Stargate-Export-Count", strconv.Itoa(n))
	w.WriteHeader(http.StatusOK)
	w.Write(b)
	return nil, nil
}
