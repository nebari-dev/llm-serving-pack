// Package gateway is a dev stand-in for the request path (Envoy AI Gateway +
// Warden): it authenticates a key, checks its budget, evaluates policy rules,
// routes to a backend with fallback, inspects the response, and emits the
// receipt the console reads. It exists so the control plane and console have
// real traffic before the full spine lands.
package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Snapshot is the config the gateway enforces, loaded from the control plane.
type Snapshot struct {
	Tenant   string
	KeyBy    map[string]*store.KeyRecord // by secret hash (current and rotating)
	Models   map[string]model.Model
	Aliases  map[string]string
	Backends []model.Backend
	Routes   []model.Route
	Budgets  map[string]model.Budget
	Rules    []model.PolicyRule // ordinal order
	Spend    store.MonthSpend
}

type Input struct {
	Secret    string
	Region    string // x-data-region header
	SessionID string
	Actor     string
	Req       fakellm.ChatRequest
	Body      []byte
	Now       time.Time
}

// Reject is a response the gateway returns without calling upstream.
type Reject struct {
	Status  int
	Code    string
	Message string
}

type Candidate struct {
	Backend model.Backend
	Model   string
}

type Decision struct {
	Reject     *Reject
	Receipt    *model.Receipt // nil when the caller isn't identified
	Req        fakellm.ChatRequest
	Candidates []Candidate

	start       time.Time
	requested   string
	rerouted    bool
	aliased     bool
	blockedBy   string
	rulesMS     float64
	budgetStep  model.TraceStep
	identity    model.TraceStep
	rulesStep   model.TraceStep
	promptToks  int
	captureCont bool
}

// Result is what one upstream attempt produced.
type Result struct {
	Status    int
	ErrMsg    string
	Content   string
	Usage     *fakellm.Usage
	TTFT      *time.Duration
	Duration  time.Duration
	Truncated bool
}

func hexStr(r *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[r.IntN(16)]
	}
	return string(b)
}

func sha(s []byte) string {
	h := sha256.Sum256(s)
	return "sha256:" + hex.EncodeToString(h[:])
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func resolveAlias(aliases map[string]string, m string) (string, bool) {
	if t, ok := aliases[m]; ok {
		return t, true
	}
	for a, t := range aliases {
		if strings.HasSuffix(a, "*") && strings.HasPrefix(m, strings.TrimSuffix(a, "*")) {
			return t, true
		}
	}
	return m, false
}

// primary is the first healthy backend serving the model.
func (s *Snapshot) primary(m string) (model.Backend, bool) {
	for _, b := range s.Backends {
		if b.Health != "down" && slices.Contains(b.Models, m) {
			return b, true
		}
	}
	return model.Backend{}, false
}

func (s *Snapshot) backend(name string) (model.Backend, bool) {
	for _, b := range s.Backends {
		if b.Name == name {
			return b, true
		}
	}
	return model.Backend{}, false
}

// routeFor picks the route whose match applies, falling back to "default".
// Only the simple match forms the demo routes use are understood.
func (s *Snapshot) routeFor(requested, team, region string) *model.Route {
	var def *model.Route
	for i := range s.Routes {
		r := &s.Routes[i]
		lhs, rhs, ok := strings.Cut(r.Match, " = ")
		if !ok {
			continue
		}
		switch {
		case lhs == "model" && rhs == "*":
			def = r
		case lhs == "model" && strings.HasSuffix(rhs, "*") && strings.HasPrefix(requested, strings.TrimSuffix(rhs, "*")),
			lhs == "model" && rhs == requested,
			lhs == "header x-data-region" && rhs == region,
			lhs == "key.team" && rhs == team:
			return r
		}
	}
	return def
}

// substitute picks the model a fallback backend should serve in place of m:
// m itself, else the priciest model of the same family, else its first model.
func (s *Snapshot) substitute(b model.Backend, m string) string {
	if slices.Contains(b.Models, m) {
		return m
	}
	fam := s.Models[m].Family
	best, price := "", -1.0
	for _, x := range b.Models {
		if s.Models[x].Family == fam && s.Models[x].InPerM > price {
			best, price = x, s.Models[x].InPerM
		}
	}
	if best != "" {
		return best
	}
	return b.Models[0]
}

type ruleCtx struct {
	team, project, key, model, provider, region string
	prompt                                      string
}

func (c ruleCtx) field(f string) string {
	switch f {
	case "team":
		return c.team
	case "project":
		return c.project
	case "key":
		return c.key
	case "model":
		return c.model
	case "provider":
		return c.provider
	case "header x-data-region":
		return c.region
	}
	return ""
}

// match evaluates a rule's conditions (all must hold). It returns the
// entities found for "contains entity" conditions.
func match(r model.PolicyRule, c ruleCtx) (bool, map[string]int) {
	found := map[string]int{}
	for _, cond := range r.When {
		switch cond.Op {
		case "contains entity":
			hit := false
			for _, e := range cond.Value {
				if n := find(e, c.prompt); n > 0 {
					found[e] += n
					hit = true
				}
			}
			if !hit {
				return false, nil
			}
		case "is", "equals":
			if !slices.Contains(cond.Value, c.field(cond.Field)) {
				return false, nil
			}
		case "is not", "not equals":
			if slices.Contains(cond.Value, c.field(cond.Field)) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, found
}

func promptText(msgs []fakellm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func money(f float64) string { return fmt.Sprintf("$%.0f", f) }

// Authenticate resolves an Authorization header value (with or without
// "Bearer ") to its key. Unknown and revoked keys, a secret retired by
// rotation, and expired keys are rejected with 401.
func Authenticate(s *Snapshot, secret string, now time.Time) (*store.KeyRecord, *Reject) {
	hash := demo.HashSecret(strings.TrimPrefix(secret, "Bearer "))
	k, ok := s.KeyBy[hash]
	if !ok || k.Status == "revoked" {
		return nil, &Reject{Status: 401, Code: "invalid_api_key", Message: "unknown or revoked API key"}
	}
	if k.Status == "rotating" && k.RotateUntil != nil && now.After(*k.RotateUntil) && k.Hash == hash && k.NextHash != "" {
		return nil, &Reject{Status: 401, Code: "invalid_api_key", Message: "this secret was retired by rotation"}
	}
	if k.ExpiresAt != nil && *k.ExpiresAt < now.UTC().Format("2006-01-02") {
		return nil, &Reject{Status: 401, Code: "expired_api_key", Message: "API key expired " + *k.ExpiresAt}
	}
	return k, nil
}

// CheckModel rejects a requested model, after aliasing, that the key's
// allowlist doesn't include.
func (s *Snapshot) CheckModel(k *store.KeyRecord, requested string) *Reject {
	return modelAllowed(k, s.Resolve(requested))
}

// Resolve maps a requested model through the aliases.
func (s *Snapshot) Resolve(requested string) string {
	m, _ := resolveAlias(s.Aliases, requested)
	return m
}

// KeyByID finds a key by id, or nil.
func (s *Snapshot) KeyByID(id string) *store.KeyRecord {
	for _, k := range s.KeyBy {
		if k.ID == id {
			return k
		}
	}
	return nil
}

// modelAllowed checks a resolved (post-alias) model against the key's allowlist.
func modelAllowed(k *store.KeyRecord, resolved string) *Reject {
	if slices.Contains(k.AllowedModels, resolved) {
		return nil
	}
	return &Reject{Status: 403, Code: "model_not_allowed", Message: fmt.Sprintf("Key %s may not call %s. Allowed: %s.", k.Name, resolved, strings.Join(k.AllowedModels, ", "))}
}

// Admit runs everything before the upstream call.
func Admit(s *Snapshot, in Input, r *rand.Rand) *Decision {
	d := &Decision{start: in.Now, requested: in.Req.Model, Req: in.Req}
	k, rej := Authenticate(s, in.Secret, in.Now)
	if rej != nil {
		d.Reject = rej
		return d
	}

	rc := &model.Receipt{
		ID: hexStr(r, 8) + "-" + hexStr(r, 4), TenantID: s.Tenant, TraceID: hexStr(r, 32),
		SessionID: in.SessionID, TS: in.Now.UnixMilli(), KeyID: k.ID, KeyName: k.Name, Team: k.Team, Project: k.Project,
		Actor: in.Actor, RequestedModel: in.Req.Model, RouteReason: "explicit", Verdict: "allowed", InboundVerdict: "allowed",
		Redactions: []model.Redaction{}, Rules: []model.RuleEval{}, RequestHash: sha(in.Body),
	}
	d.Receipt = rc
	d.identity = model.TraceStep{Step: "Identity resolved", Input: "Bearer " + k.Prefix + "…", Outcome: k.Name + " → " + k.Team + " / " + k.Project, MS: 0.3, State: "ok"}
	for _, m := range in.Req.Messages {
		d.promptToks += fakellm.EstimateTokens(m.Content) + 4
	}

	// Budget (§5.2): spend is month to date from the aggregates.
	d.budgetStep = model.TraceStep{Step: "Budget checked", Input: "no budget attached", Outcome: "skipped", MS: 0.1, State: "skip"}
	if b, ok := s.Budgets[k.BudgetID]; ok {
		spent := s.Spend.ByTeam[b.Scope]
		if b.ScopeType == "key" {
			spent = 0
			for _, kk := range s.KeyBy {
				if kk.Name == b.Scope {
					spent = s.Spend.ByKey[kk.ID]
					break
				}
			}
		}
		d.budgetStep.Input = fmt.Sprintf("budget %s · %s of %s", b.Scope, money(spent), money(b.CapUSD))
		d.budgetStep.Outcome, d.budgetStep.State = "within cap", "ok"
		if spent >= b.CapUSD {
			switch b.OnExceed {
			case "block":
				d.budgetStep.Outcome, d.budgetStep.State = "over cap · blocked", "fail"
				return d.block(429, "budget_exceeded", fmt.Sprintf("Budget %s is over its %s monthly cap. Ask a finance admin to raise it.", b.Scope, money(b.CapUSD)))
			case "throttle":
				d.budgetStep.Outcome, d.budgetStep.State = "over cap · throttle active, admitted", "warn"
			default:
				d.budgetStep.Outcome, d.budgetStep.State = "over cap · warning only", "warn"
			}
		}
	}

	resolved, aliased := resolveAlias(s.Aliases, in.Req.Model)
	d.aliased = aliased
	if aliased {
		rc.RouteReason = "alias"
	}
	if rej := modelAllowed(k, resolved); rej != nil {
		return d.block(rej.Status, rej.Code, rej.Message)
	}
	if _, ok := s.Models[resolved]; !ok {
		return d.block(404, "model_not_found", "unknown model "+resolved)
	}
	current, ok := s.primary(resolved)
	if !ok {
		return d.block(503, "no_healthy_backend", "no healthy backend serves "+resolved)
	}

	// Rules (§5.3): ordinal order; the first block wins, redacts accumulate,
	// reroute is last-write-wins. Monitor-mode rules only record "would".
	msgs := slices.Clone(in.Req.Messages)
	var outcomes []string
	for _, rule := range s.Rules {
		if rule.Mode == "draft" {
			continue
		}
		t0 := time.Now()
		ok, found := match(rule, ruleCtx{team: k.Team, project: k.Project, key: k.Name, model: resolved, provider: current.Provider, region: in.Region, prompt: promptText(msgs)})
		ev := model.RuleEval{RuleID: rule.ID, Name: rule.Name, Version: rule.Version, Matched: ok, Action: "no match"}
		if ok && len(rule.Then) > 0 {
			act := rule.Then[0]
			ev.Action = act.Action
			if rule.Mode == "monitor" {
				ev.Action = "would " + act.Action
			} else {
				switch act.Action {
				case "block":
					ent := firstKey(found)
					d.blockedBy = fmt.Sprintf("blocked by %s v%d on entity %q", rule.Name, rule.Version, ent)
					ev.MS = round2(float64(time.Since(t0).Microseconds())/1000 + 0.1)
					rc.Rules = append(rc.Rules, ev)
					d.rulesMS += ev.MS
					return d.block(403, "policy_blocked", fmt.Sprintf("Rule %s v%d matched entity %q. Remove it from the prompt, or route through a self-hosted backend.", rule.Name, rule.Version, ent))
				case "redact":
					for ent, n := range found {
						for i := range msgs {
							msgs[i].Content = redact(ent, msgs[i].Content)
						}
						rc.Redactions = append(rc.Redactions, model.Redaction{Type: ent, Count: n})
						outcomes = append(outcomes, fmt.Sprintf("redacted %d %s", n, ent))
					}
				case "route to":
					if _, isModel := s.Models[act.Detail]; isModel {
						if b, ok := s.primary(act.Detail); ok {
							resolved, current = act.Detail, b
						}
					} else {
						for _, b := range s.Backends {
							if b.Region == act.Detail && b.Health != "down" {
								resolved, current = s.substitute(b, resolved), b
								break
							}
						}
					}
					d.rerouted = true
					outcomes = append(outcomes, fmt.Sprintf("%s matched → route to %s", rule.Name, act.Detail))
				}
			}
		}
		ev.MS = round2(float64(time.Since(t0).Microseconds())/1000 + 0.1)
		d.rulesMS += ev.MS
		rc.Rules = append(rc.Rules, ev)
	}
	if d.rerouted {
		rc.RouteReason = "policy"
	}
	d.Req.Messages = msgs
	d.Req.Model = resolved
	d.rulesStep = model.TraceStep{Step: "Rules evaluated", Input: fmt.Sprintf("%d rules", len(rc.Rules)), Outcome: "no rule matched", MS: round2(d.rulesMS), State: "ok"}
	if len(outcomes) > 0 {
		d.rulesStep.Outcome, d.rulesStep.State = strings.Join(outcomes, " · "), "warn"
	}

	// Candidates: the chosen backend, then other healthy backends for the same
	// model, then the matching route's fallback list.
	d.Candidates = []Candidate{{current, resolved}}
	seen := map[string]bool{current.Name: true}
	for _, b := range s.Backends {
		if !seen[b.Name] && b.Health != "down" && slices.Contains(b.Models, resolved) {
			d.Candidates = append(d.Candidates, Candidate{b, resolved})
			seen[b.Name] = true
		}
	}
	if rt := s.routeFor(in.Req.Model, k.Team, in.Region); rt != nil && !d.rerouted {
		for _, name := range rt.Fallback {
			if b, ok := s.backend(name); ok && !seen[name] && b.Health != "down" {
				d.Candidates = append(d.Candidates, Candidate{b, s.substitute(b, resolved)})
				seen[name] = true
			}
		}
	}
	return d
}

func firstKey(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func (d *Decision) block(status int, code, msg string) *Decision {
	d.Reject = &Reject{Status: status, Code: code, Message: msg}
	return d
}

// Retryable reports whether a failed attempt should move to the next candidate.
func Retryable(status int) bool {
	return status == 529 || status == 503 || status == 502 || status == 500
}

// Finish fills in the receipt from the upstream outcome. attempts lists the
// backends that failed before the final one (empty when the first worked).
func (d *Decision) Finish(s *Snapshot, final *Candidate, res Result, failed []string, now time.Time) *model.Receipt {
	rc := d.Receipt
	if rc == nil {
		return nil
	}
	rc.InputTokens = d.promptToks
	trace := []model.TraceStep{d.identity, d.budgetStep}

	if d.Reject != nil {
		rc.Verdict, rc.Status, rc.ErrorCode, rc.ErrorDetail = "blocked", d.Reject.Status, d.Reject.Code, d.Reject.Message
		rc.ResolvedModel = d.Req.Model
		if r, ok := resolveAlias(s.Aliases, d.Req.Model); ok {
			rc.ResolvedModel = r
		}
		if b, ok := s.primary(rc.ResolvedModel); ok {
			rc.Backend, rc.Provider, rc.Region = b.Name, b.Provider, b.Region
		} else {
			rc.Backend, rc.Provider, rc.Region = "—", "—", "—"
		}
		rc.ResponseHash = "—"
		rc.DurationMS = int(now.Sub(d.start).Milliseconds()) + 18
		rules := model.TraceStep{Step: "Rules evaluated", Input: fmt.Sprintf("%d rules", len(rc.Rules)), Outcome: "not reached", MS: round2(d.rulesMS), State: "skip"}
		if d.blockedBy != "" {
			rules.Outcome, rules.State = d.blockedBy, "fail"
		} else if d.budgetStep.State != "fail" {
			rules.Outcome, rules.State = d.Reject.Message, "fail"
		}
		skip := func(step string) model.TraceStep {
			return model.TraceStep{Step: step, Input: "—", Outcome: "not reached", State: "skip"}
		}
		rc.Trace = append(trace, rules, skip("Route selected"), skip("Upstream called"), skip("Response inspected"))
		return rc
	}

	b, m := final.Backend, final.Model
	rc.ResolvedModel, rc.Backend, rc.Provider, rc.Region = m, b.Name, b.Provider, b.Region
	if len(failed) > 0 {
		rc.FallbackFrom, rc.RouteReason = failed[0], "fallback"
	}
	rc.Status, rc.DurationMS = res.Status, int(res.Duration.Milliseconds())
	if res.TTFT != nil {
		t := int(res.TTFT.Milliseconds())
		rc.TTFTMS = &t
	}
	if res.Usage != nil {
		rc.InputTokens = res.Usage.PromptTokens
		rc.CachedInputTokens = res.Usage.PromptTokensDetails.CachedTokens
		rc.OutputTokens = res.Usage.CompletionTokens
		rc.ReasoningTokens = res.Usage.CompletionTokensDetails.ReasoningTokens
	} else if res.Status == 200 {
		rc.OutputTokens = fakellm.EstimateTokens(res.Content) // stream cut before usage arrived
	}
	if res.Status == 200 {
		p := s.Models[m]
		rc.CostBasis = &p
		rc.CostUSD = (float64(rc.InputTokens-rc.CachedInputTokens)*p.InPerM + float64(rc.CachedInputTokens)*p.CachedPerM +
			float64(rc.OutputTokens)*p.OutPerM + float64(rc.ReasoningTokens)*p.ReasoningPerM) / 1_000_000
	} else {
		rc.ErrorCode = "upstream_error"
		if res.Status == 429 {
			rc.ErrorCode = "upstream_rate_limited"
		}
		rc.ErrorDetail = fmt.Sprintf("%s returned %d: %s", b.Name, res.Status, res.ErrMsg)
	}
	rc.ResponseHash = sha([]byte(res.Content))

	switch {
	case res.Truncated:
		rc.Verdict, rc.InboundVerdict = "truncated", "blocked"
	case len(rc.Redactions) > 0:
		rc.Verdict = "redacted"
	case d.rerouted:
		rc.Verdict = "rerouted"
	}
	if b.CaptureContent {
		rc.ContentCaptured = true
		rc.Content = map[string]any{"messages": d.Req.Messages, "response": res.Content}
	}

	route := model.TraceStep{Step: "Route selected", Input: "requested " + d.requested, Outcome: m + " via " + b.Name, MS: 0.2, State: "ok"}
	if len(failed) > 0 {
		route.Outcome += fmt.Sprintf(" (fallback from %s)", strings.Join(failed, ", "))
		route.State = "warn"
	}
	stream := ""
	if d.Req.Stream {
		stream = " · stream"
	}
	up := model.TraceStep{Step: "Upstream called", Input: b.Provider + " · " + b.Region + stream, Outcome: fmt.Sprintf("%d · %d output tokens", res.Status, rc.OutputTokens), MS: float64(rc.DurationMS), State: "ok"}
	if res.Status != 200 {
		up.Outcome, up.State = fmt.Sprintf("%d %s", res.Status, res.ErrMsg), "fail"
	}
	insp := model.TraceStep{Step: "Response inspected", Input: "exfil URLs", Outcome: "clean", MS: 0.4, State: "ok"}
	if res.Truncated {
		insp.Outcome, insp.State = fmt.Sprintf("exfil URL pattern at token %d · stream cut", rc.OutputTokens), "fail"
	} else if res.Status != 200 {
		insp.Outcome, insp.State = "not reached", "skip"
	}
	rc.Trace = append(trace, d.rulesStep, route, up, insp)
	rc.InFlight = false
	return rc
}

// Inspector watches streamed content and cuts it at the first exfil URL.
type Inspector struct {
	buf strings.Builder
	Cut bool
}

// Feed returns the part of delta that may be sent, and false once cut.
func (in *Inspector) Feed(delta string) (string, bool) {
	start := in.buf.Len()
	in.buf.WriteString(delta)
	if loc := exfil.FindStringIndex(in.buf.String()); loc != nil {
		in.Cut = true
		keep := max(loc[0]-start, 0)
		s := in.buf.String()[:loc[0]]
		in.buf.Reset()
		in.buf.WriteString(s)
		return delta[:min(keep, len(delta))], false
	}
	return delta, true
}

func (in *Inspector) Content() string { return in.buf.String() }
