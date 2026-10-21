package openmeter

import (
	"context"
	"strconv"
	"strings"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// The collector sends one llm.request event per gateway request, and one
// llm.tokens event per token type with a count, its ID the request's plus
// ":<type>".
const (
	requestEvent = "llm.request"
	tokensEvent  = "llm.tokens"
)

// listReceipts builds the newest limit receipts. Token events can come before
// or after their request's in the page order, so it keeps reading until it
// has limit requests and every one of their token events is past.
func (s *Server) listReceipts(ctx context.Context, limit int) ([]model.Receipt, error) {
	var order []string
	byID := map[string]*model.Receipt{}
	tokens := map[string]map[string]int{}
	var oldest int64
	err := s.Client.EachEvent(ctx, func(e Event) bool {
		ms := e.Time.UnixMilli()
		if len(order) >= limit && ms < oldest {
			return false
		}
		switch e.Type {
		case requestEvent:
			if len(order) >= limit {
				return true
			}
			r := receiptFrom(e)
			byID[e.ID] = &r
			order = append(order, e.ID)
			oldest = ms
		case tokensEvent:
			id, typ, ok := strings.Cut(e.ID, ":")
			if !ok {
				return true
			}
			if tokens[id] == nil {
				tokens[id] = map[string]int{}
			}
			tokens[id][typ] = intOf(e.Data["tokens"])
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	prices, err := s.newPricer(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Receipt, 0, len(order))
	for _, id := range order {
		r := byID[id]
		t := tokens[id]
		r.CachedInputTokens = t["cache_read"]
		r.CacheWriteTokens = t["cache_write"]
		r.InputTokens = t["input"] + t["cache_read"] + t["cache_write"]
		r.OutputTokens = t["output"]
		r.ReasoningTokens = t["reasoning"]
		if r.Verdict != "truncated" {
			if r.CostUSD, err = prices.cost(ctx, r.Backend, r.ResolvedModel, t); err != nil {
				return nil, err
			}
			if r.CostUSD != nil {
				*r.CostUSD = roundUSD(*r.CostUSD)
			}
		}
		out = append(out, *r)
	}
	return out, nil
}

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
		Verdict:        "allowed",
		InboundVerdict: "skipped",
		Redactions:     []model.Redaction{},
		Rules:          []model.RuleEval{},
		Trace:          []model.TraceStep{},
	}
	if str(d["usage_reported"]) == "false" {
		// A stream the caller cancelled, or a response the gateway couldn't
		// read usage from: the request happened, its tokens are unknown.
		r.Verdict = "truncated"
		r.ErrorCode = "no_usage_reported"
		r.ErrorDetail = "The gateway reported no token counts for this request (Envoy response flags: " + str(d["response_flags"]) + ")."
	}
	return r
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
