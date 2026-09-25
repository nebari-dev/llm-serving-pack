// Package ingest turns Agent Router access-log records into receipts (spec
// §4.6). The gateway ships one OTLP log record per LLM request, with the
// attributes named in aigw/config.yaml; the control plane's config fills in
// what the gateway doesn't know (provider, region, pricing).
//
// Keys are checked by stargate-api's ext_authz service, which passes the key,
// team and project on as headers the access log records. A request it blocks
// for its model is logged with the identity from the 403 instead. Warden
// (cmd/warden) evaluates budgets and rules next and leaves its decision in
// dynamic metadata, which the access log carries as stargate.policy: the
// verdict, rules, redactions and the trace steps it ran. Without Warden in the
// path, every request that reached a backend is "allowed".
package ingest

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Attribute names, as set in the EnvoyProxy access-log format.
const (
	attrStart       = "start_time"
	attrDuration    = "duration"
	attrFirstByte   = "response_duration"
	attrStatus      = "response_code"
	attrFlags       = "response_flags"
	attrAttempts    = "upstream_request_attempt_count"
	attrRequestID   = "x-request-id"
	attrTraceparent = "traceparent"
	attrSession     = "session.id"
	attrReqModel    = "gen_ai.request.model"
	attrRespModel   = "gen_ai.response.model"
	attrBackend     = "gen_ai.provider.name"
	attrInput       = "gen_ai.usage.input_tokens"
	attrCached      = "gen_ai.usage.cached_input_tokens"
	attrOutput      = "gen_ai.usage.output_tokens"
	attrReasoning   = "gen_ai.usage.reasoning_tokens"
	attrKeyID       = "stargate.key_id"
	attrTeam        = "stargate.team"
	attrProject     = "stargate.project"
	attrDeniedKeyID = "stargate.denied_key_id"
	attrDeniedModel = "stargate.denied_model"
	attrPolicy      = "stargate.policy"
)

// Receipt builds a settled receipt from one access-log record's attributes.
func Receipt(s *gateway.Snapshot, a map[string]string) (*model.Receipt, error) {
	get := func(k string) string {
		v := a[k]
		if v == "-" {
			return ""
		}
		return v
	}
	num := func(k string) int {
		n, _ := strconv.Atoi(get(k))
		return n
	}

	reqID := strings.ReplaceAll(get(attrRequestID), "-", "")
	if len(reqID) < 12 {
		return nil, errors.New("record has no x-request-id")
	}
	start, err := time.Parse(time.RFC3339Nano, get(attrStart))
	if err != nil {
		return nil, fmt.Errorf("start_time: %w", err)
	}

	rc := &model.Receipt{
		// Derived from the request id so a redelivered record upserts the same row.
		ID: reqID[:8] + "-" + reqID[8:12], TenantID: s.Tenant, TraceID: traceID(get(attrTraceparent), reqID),
		SessionID: get(attrSession), TS: start.UnixMilli(), DurationMS: num(attrDuration),
		KeyName: "unauthenticated", RequestedModel: get(attrReqModel), ResolvedModel: get(attrRespModel),
		RouteReason: "explicit", Verdict: "allowed", InboundVerdict: "allowed",
		InputTokens: num(attrInput), CachedInputTokens: num(attrCached), OutputTokens: num(attrOutput), ReasoningTokens: num(attrReasoning),
		Status: num(attrStatus), Redactions: []model.Redaction{}, Rules: []model.RuleEval{},
	}
	if rc.ResolvedModel == "" {
		rc.ResolvedModel = rc.RequestedModel
	}
	if fb := get(attrFirstByte); fb != "" {
		n, _ := strconv.Atoi(fb)
		rc.TTFTMS = &n
	}

	// gen_ai.provider.name is the AIServiceBackend's route-scoped name:
	// "default/openai-prod/route/aigw-run/rule/0/ref/0".
	if parts := strings.Split(get(attrBackend), "/"); len(parts) > 1 {
		rc.Backend = parts[1]
	}
	for _, b := range s.Backends {
		if b.Name == rc.Backend {
			rc.Provider, rc.Region = b.Provider, b.Region
		}
	}

	identity := model.TraceStep{Step: "Identity resolved", Input: "—", Outcome: "no key check in the path", State: "skip"}
	keyID, denied := KeyID(a), get(attrKeyID) == ""
	if keyID != "" {
		rc.KeyID, rc.KeyName, rc.Team, rc.Project = keyID, keyID, get(attrTeam), get(attrProject)
		k := s.KeyByID(keyID)
		if k != nil {
			rc.KeyName, identity.Input = k.Name, "Bearer "+k.Prefix+"…"
			if denied {
				// A 403 has no request headers to log; the key has the rest.
				rc.Team, rc.Project = k.Team, k.Project
			}
		}
		identity.Outcome, identity.State = rc.KeyName+" → "+rc.Team+" / "+rc.Project, "ok"
		if denied {
			return blocked(s, rc, k, get(attrDeniedModel), identity), nil
		}
	}

	var policy []model.TraceStep
	if raw := get(attrPolicy); raw != "" {
		var p gateway.Policy
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("%s: %w", attrPolicy, err)
		}
		if b := withPolicy(rc, p); b {
			rc.Trace = append([]model.TraceStep{identity}, p.Trace...)
			return rc, nil
		}
		policy = p.Trace
	}
	route := model.TraceStep{Step: "Route selected", Input: "requested " + rc.RequestedModel, Outcome: rc.ResolvedModel + " via " + rc.Backend, State: "ok"}
	if attempts := num(attrAttempts); attempts > 1 {
		route.Outcome += fmt.Sprintf(" after %d attempts", attempts)
		route.State = "warn"
	}
	up := model.TraceStep{Step: "Upstream called", Input: rc.Provider + " · " + rc.Region, MS: float64(rc.DurationMS),
		Outcome: fmt.Sprintf("%d · %d output tokens", rc.Status, rc.OutputTokens), State: "ok"}

	switch {
	case rc.Backend == "":
		// Agent Router answered without calling a backend, e.g. no route for the model.
		rc.Verdict, rc.ErrorCode = "blocked", "no_route"
		rc.ErrorDetail = fmt.Sprintf("no route for model %q (%d)", rc.RequestedModel, rc.Status)
		route.Outcome, route.State = "no matching route", "fail"
		up.Outcome, up.State = "not reached", "skip"
	case rc.Status == 0 && strings.Contains(get(attrFlags), "DC"):
		// Envoy's DC flag: the caller hung up before the response finished.
		rc.ErrorCode, rc.ErrorDetail = "client_disconnected", "caller disconnected before "+rc.Backend+" responded"
		up.Outcome, up.State = "caller disconnected", "warn"
	case rc.Status != 200:
		rc.ErrorCode = "upstream_error"
		if rc.Status == 429 {
			rc.ErrorCode = "upstream_rate_limited"
		}
		rc.ErrorDetail = fmt.Sprintf("%s returned %d", rc.Backend, rc.Status)
		if f := get(attrFlags); f != "" {
			rc.ErrorDetail += " (" + f + ")"
		}
		up.Outcome, up.State = fmt.Sprintf("%d", rc.Status), "fail"
	default:
		if p, ok := s.Models[rc.ResolvedModel]; ok {
			rc.CostBasis = &p
			rc.CostUSD = (float64(rc.InputTokens-rc.CachedInputTokens)*p.InPerM + float64(rc.CachedInputTokens)*p.CachedPerM +
				float64(rc.OutputTokens)*p.OutPerM + float64(rc.ReasoningTokens)*p.ReasoningPerM) / 1_000_000
		}
	}
	rc.Trace = append(append([]model.TraceStep{identity}, policy...), route, up)
	return rc, nil
}

// withPolicy lays Warden's decision over the access-log fields, and reports
// whether Warden refused the request (then the receipt is complete).
func withPolicy(rc *model.Receipt, p gateway.Policy) bool {
	rc.Verdict, rc.Rules, rc.Redactions = p.Verdict, p.Rules, p.Redactions
	// Agent Router logs the model after Warden's reroute; the caller asked for this one.
	rc.RequestedModel = cmp.Or(p.RequestedModel, rc.RequestedModel)
	rc.RouteReason = cmp.Or(p.RouteReason, rc.RouteReason)
	rc.RequestHash = cmp.Or(p.RequestHash, rc.RequestHash)
	rc.Actor, rc.SessionID = cmp.Or(p.Actor, rc.Actor), cmp.Or(rc.SessionID, p.SessionID)
	b := p.Blocked
	if b == nil {
		return false
	}
	rc.Verdict, rc.Status, rc.ErrorCode, rc.ErrorDetail = "blocked", b.Status, b.ErrorCode, b.ErrorDetail
	rc.ResolvedModel, rc.Backend, rc.Provider, rc.Region = b.ResolvedModel, b.Backend, b.Provider, b.Region
	rc.InputTokens, rc.ResponseHash, rc.TTFTMS = b.InputTokens, "—", nil
	return true
}

// KeyID is the key the key check resolved for a record, admitted or blocked,
// or "" when there was none.
func KeyID(a map[string]string) string {
	for _, k := range []string{attrKeyID, attrDeniedKeyID} {
		if v := a[k]; v != "" && v != "-" {
			return v
		}
	}
	return ""
}

// blocked fills in a request ext_authz refused with 403: a model the key may
// not call. It never reached a route or a backend.
func blocked(s *gateway.Snapshot, rc *model.Receipt, k *store.KeyRecord, requested string, identity model.TraceStep) *model.Receipt {
	rc.Verdict, rc.ErrorCode = "blocked", "model_not_allowed"
	rc.RequestedModel, rc.ResolvedModel = requested, s.Resolve(requested)
	rc.ErrorDetail = "model " + rc.ResolvedModel + " is not allowed for this key"
	if k != nil {
		if rej := s.CheckModel(k, requested); rej != nil {
			rc.ErrorDetail = rej.Message
		}
	}
	rc.Backend, rc.Provider, rc.Region, rc.ResponseHash = "—", "—", "—", "—"
	skip := func(step string) model.TraceStep {
		return model.TraceStep{Step: step, Input: "—", Outcome: "not reached", State: "skip"}
	}
	check := model.TraceStep{Step: "Model allowed", Input: "requested " + requested, Outcome: rc.ErrorDetail, State: "fail"}
	rc.Trace = []model.TraceStep{identity, check, skip("Route selected"), skip("Upstream called")}
	return rc
}

// traceID pulls the trace id out of a W3C traceparent: "00-<trace>-<span>-<flags>".
// Agent Router only sets traceparent when tracing is on; otherwise the request
// id (a UUID, also 32 hex) stands in so every receipt has one.
func traceID(tp, reqID string) string {
	if parts := strings.Split(tp, "-"); len(parts) == 4 {
		return parts[1]
	}
	return reqID
}
