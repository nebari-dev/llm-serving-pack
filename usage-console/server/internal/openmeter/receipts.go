package openmeter

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// The collector sends one llm.request event per gateway request, and one
// llm.tokens event per token type with a count, its ID the request's plus
// ":<type>" and its time the request's. A request ID can hold colons; a
// token type can't.
const (
	requestEvent = "llm.request"
	tokensEvent  = "llm.tokens"
)

// receiptScanCap is how many request events one read looks through for
// matches before it stops and says how far back it got.
const receiptScanCap = 5000

// tokenLookupBatch is how many receipts' token events one OpenMeter call
// fetches by ID.
const tokenLookupBatch = 20

// listReceipts builds the newest limit receipts that match f. It reads only
// request events in f's window, newest first, then the token events of the
// ones that match, by ID. When it looks through receiptScanCap request
// events without finding limit matches, it stops and continueBefore (epoch
// ms, exclusive) is where a further read picks up: older matches may exist.
// It overlaps the last millisecond read, so repeats are possible.
func (s *Server) listReceipts(ctx context.Context, limit int, f receiptFilter) (out []model.Receipt, continueBefore int64, err error) {
	out, continueBefore, err = s.scanRequests(ctx, limit, f)
	if err != nil {
		return nil, 0, err
	}
	if err := s.complete(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, continueBefore, nil
}

// scanRequests is listReceipts' first step: the matching request events as
// receipts, without their tokens or cost.
func (s *Server) scanRequests(ctx context.Context, limit int, f receiptFilter) (out []model.Receipt, continueBefore int64, err error) {
	q := eventQuery{typ: requestEvent}
	if f.since > 0 {
		q.since = time.UnixMilli(f.since)
	}
	if f.before > 0 {
		q.before = time.UnixMilli(f.before)
	}
	out = []model.Receipt{}
	scanned := 0
	err = s.Client.EachEvent(ctx, q, func(e Event) bool {
		scanned++
		if r := receiptFrom(e); f.matchReceipt(r) {
			out = append(out, r)
		}
		if len(out) < limit && scanned >= receiptScanCap {
			continueBefore = e.Time.UnixMilli() + 1
			return false
		}
		return len(out) < limit
	})
	return out, continueBefore, err
}

// complete fills in the receipts' token counts and, for served requests,
// their cost.
func (s *Server) complete(ctx context.Context, rs []model.Receipt) error {
	tokens, err := s.tokensOf(ctx, rs)
	if err != nil {
		return err
	}
	prices, err := s.pricer(ctx)
	if err != nil {
		return err
	}
	for i := range rs {
		r := &rs[i]
		t := tokens[r.ID]
		r.CachedInputTokens = t["cache_read"]
		r.CacheWriteTokens = t["cache_write"]
		r.InputTokens = t["input"] + t["cache_read"] + t["cache_write"]
		r.OutputTokens = t["output"]
		r.ReasoningTokens = t["reasoning"]
		if r.Verdict != "allowed" {
			continue
		}
		if r.CostUSD, err = prices.cost(ctx, r.Backend, r.ResolvedModel, t); err != nil {
			return err
		}
		if r.CostUSD != nil {
			*r.CostUSD = roundUSD(*r.CostUSD)
		}
	}
	return nil
}

// tokensOf reads the receipts' llm.tokens events by ID, a batch of receipts
// a call, and returns each receipt's count by token type.
func (s *Server) tokensOf(ctx context.Context, rs []model.Receipt) (map[string]map[string]int, error) {
	out := make(map[string]map[string]int, len(rs))
	for start := 0; start < len(rs); start += tokenLookupBatch {
		var ids []string
		for _, r := range rs[start:min(start+tokenLookupBatch, len(rs))] {
			for _, typ := range tokenTypes {
				ids = append(ids, r.ID+":"+typ)
			}
		}
		err := s.Client.EachEvent(ctx, eventQuery{typ: tokensEvent, ids: ids}, func(e Event) bool {
			if i := strings.LastIndex(e.ID, ":"); i >= 0 {
				id, typ := e.ID[:i], e.ID[i+1:]
				if out[id] == nil {
					out[id] = map[string]int{}
				}
				out[id][typ] = intOf(e.Data["tokens"])
			}
			return true
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// tokenTypes are the token types an llm.tokens event can carry, as priced
// (tokenRate); the collector sends input, cache_read, cache_write, output.
var tokenTypes = []string{"input", "cache_read", "cache_write", "output", "reasoning"}

func receiptFrom(e Event) model.Receipt {
	d := e.Data
	status, _ := strconv.Atoi(str(d["response_code"]))
	r := model.Receipt{
		ID:             e.ID,
		TraceID:        e.ID,
		TS:             e.Time.UnixMilli(),
		DurationMS:     intOf(d["duration_ms"]),
		KeyID:          str(d["api_key"]),
		KeyName:        str(d["api_key"]),
		Team:           str(d["team"]),
		Project:        str(d["model_resource"]),
		ProjectID:      projectID(str(d["team"]), str(d["model_resource"])),
		Actor:          str(d["user"]),
		RequestedModel: str(d["model"]),
		ResolvedModel:  str(d["model"]),
		Backend:        str(d["model_resource"]),
		Status:         status,
		Verdict:        verdictOf(status, str(d["usage_reported"])),
	}
	switch r.Verdict {
	case "throttled":
		r.ErrorCode = "rate_limited"
	case "blocked":
		r.ErrorCode = "refused"
		r.ErrorDetail = "The gateway refused this request (HTTP " + strconv.Itoa(status) + ")."
	case "truncated":
		r.ErrorCode = "no_usage_reported"
		r.ErrorDetail = "The gateway reported no token counts for this request (Envoy response flags: " + str(d["response_flags"]) + ")."
	}
	return r
}

// verdictOf is a request's outcome from its status and whether the gateway
// reported its usage.
func verdictOf(status int, usageReported string) string {
	switch {
	case status == http.StatusTooManyRequests:
		// Refused before it reached the model, as the traffic chart counts it.
		return "throttled"
	case status >= 400:
		return "blocked"
	case usageReported == "false":
		// A stream the caller cancelled, or a response the gateway couldn't
		// read usage from: the request happened, its tokens are unknown.
		return "truncated"
	}
	return "allowed"
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// intOf reads a JSON number, or a number sent as a string.
func intOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}
