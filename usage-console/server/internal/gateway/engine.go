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
	"github.com/jbouder/stargate/server/internal/pricing"
	"github.com/jbouder/stargate/server/internal/store"
)

// Snapshot is the config the gateway enforces, loaded from the control plane.
type Snapshot struct {
	Tenant   string
	KeyBy    map[string]*store.KeyRecord // by secret hash (current and rotating)
	Models   map[string]model.Model
	Prices   map[Pair][]store.PriceRow // each pair's rows, oldest first
	Aliases  map[string]string
	Backends []model.Backend
	Routes   []model.Route
	Budgets  map[string]model.Budget
	Rules    []model.PolicyRule // ordinal order
	Spend    store.MonthSpend
	// Detectors is what "contains entity" can name: built-ins plus the
	// tenant's custom entities. Nil is the built-ins.
	Detectors *Detectors
}

// Pair is a (model, backend), which is what a price belongs to.
type Pair struct{ Model, Backend string }

// WithPrices is a copy of s that prices from rows (store.PriceRowsSince)
// instead of the ones it loaded with.
func (s *Snapshot) WithPrices(rows []store.PriceRow) *Snapshot {
	c := *s
	c.Prices = map[Pair][]store.PriceRow{}
	for _, p := range rows {
		k := Pair{p.ModelID, p.Backend}
		c.Prices[k] = append(c.Prices[k], p)
	}
	for _, ps := range c.Prices {
		slices.SortFunc(ps, func(a, b store.PriceRow) int { return a.From.Compare(b.From) })
	}
	return &c
}

// Cost prices a served request at its pair's row in effect when it started
// (spec §5.1): nil, nil when there was no price then (or the row lacks a
// rate the tokens need).
func (s *Snapshot) Cost(modelID, backend string, at time.Time, t pricing.Tokens) (*float64, *model.CostBasis) {
	row, ok := store.PriceAt(s.Prices[Pair{modelID, backend}], at)
	if !ok {
		return nil, nil
	}
	c := pricing.Cost(row.Rates, t)
	if c == nil {
		return nil, nil
	}
	return c, store.Basis(s.Models[modelID], row)
}

// TokensOf is a receipt's token counts for pricing.
func TokensOf(rc *model.Receipt) pricing.Tokens {
	return pricing.Tokens{Input: rc.InputTokens, Cached: rc.CachedInputTokens, CacheWrite: rc.CacheWriteTokens, Output: rc.OutputTokens, Reasoning: rc.ReasoningTokens}
}

type Input struct {
	Secret    string
	Region    string // x-data-region header
	SessionID string
	Actor     string
	Req       fakellm.ChatRequest
	Body      []byte
	Now       time.Time
	// Deadline, when set, bounds rule evaluation (§9.3): a rule reached after
	// it isn't evaluated, and its fail mode decides the request.
	Deadline time.Time
	// Throttle counts each key's requests while a throttle budget is over
	// its cap. It must outlive the request; nil remembers nothing.
	Throttle *Throttle
}

// Reject is a response the gateway returns without calling upstream.
type Reject struct {
	Status  int
	Code    string
	Message string
	// RetryAfter, when set, is the Retry-After header in seconds: when a
	// throttled key's next slot opens.
	RetryAfter int
}

// Throttled reports whether the refusal is a throttle's: "try again later",
// recorded with its own verdict rather than as a block.
func (r *Reject) Throttled() bool { return r != nil && r.Code == "budget_throttled" }

type Candidate struct {
	Backend model.Backend
	Model   string
}

type Decision struct {
	Reject     *Reject
	Receipt    *model.Receipt // nil when the caller isn't identified
	Req        fakellm.ChatRequest
	Candidates []Candidate
	// Vault holds what redactions by rules that rehydrate on return replaced,
	// for the response to put back.
	Vault *Vault

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
	// pastDeadline: a rule wasn't evaluated in time and its fail mode decided.
	pastDeadline bool
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

// routeFor picks the route the gateway would: the one with the most header
// conditions that all hold, then the first in rule order. The dev gateway
// knows only the key's team (x-stargate-team) and x-data-region.
func (s *Snapshot) routeFor(requested, team, region string) *model.Route {
	known := map[string]string{"x-stargate-team": team, "x-data-region": region}
	var best *model.Route
	for i := range s.Routes {
		r := &s.Routes[i]
		ok := slices.ContainsFunc(r.Match.Models, func(m string) bool {
			return m == requested || m == "*" || strings.HasSuffix(m, "*") && strings.HasPrefix(requested, strings.TrimSuffix(m, "*"))
		})
		for _, h := range r.Match.Headers {
			if v, k := known[h.Name]; !k || v != h.Value {
				ok = false
			}
		}
		if ok && (best == nil || len(r.Match.Headers) > len(best.Match.Headers)) {
			best = r
		}
	}
	return best
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
		in := 0.0
		p, _ := store.PriceAt(s.Prices[Pair{x, b.Name}], time.Now())
		if r := p.Rates[pricing.Input]; r != nil {
			in = *r
		}
		if s.Models[x].Family == fam && in > price {
			best, price = x, in
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
	det                                         *Detectors
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
				if n := c.det.find(e, c.prompt); n > 0 {
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

// money is whole dollars, with cents under $100 so a small cap doesn't read $0.
func money(f float64) string {
	if f < 100 {
		return fmt.Sprintf("$%.2f", f)
	}
	return fmt.Sprintf("$%.0f", f)
}

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
	m, _ := store.ResolveAlias(s.Aliases, requested)
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

// governingBudget picks the budget that decides a request for k. Every budget
// whose scope covers the key applies: its team, its project, the key itself. The strictest over-cap one
// wins (block, then throttle, then warn); with none over cap it's the one
// nearest its cap, so the trace shows the tightest headroom.
func (s *Snapshot) governingBudget(k *store.KeyRecord) (model.Budget, float64, bool) {
	severity := map[string]int{"warn": 1, "throttle": 2, "block": 3}
	var best model.Budget
	var bestSpent float64
	bestRank, bestRatio, found := -1, 0.0, false
	for _, b := range s.Budgets {
		if !store.BudgetCovers(b, k.APIKey) {
			continue
		}
		var spent float64
		switch b.ScopeType {
		case "team":
			spent = s.Spend.ByTeam[b.Scope]
		case "project":
			spent = s.projectSpend(b.Scope)
		case "key":
			spent = s.Spend.ByKey[k.ID]
		}
		rank, ratio := 0, 0.0
		if spent >= b.CapUSD {
			rank = severity[b.OnExceed]
		}
		if b.CapUSD > 0 {
			ratio = spent / b.CapUSD
		}
		if !found || rank > bestRank || rank == bestRank && (ratio > bestRatio || ratio == bestRatio && b.ID < best.ID) {
			best, bestSpent, bestRank, bestRatio, found = b, spent, rank, ratio, true
		}
	}
	return best, bestSpent, found
}

// budgetName is how traces and messages name a budget covering k: by the
// key's or project's name, not the id its scope holds.
func budgetName(b model.Budget, k *store.KeyRecord) string {
	switch b.ScopeType {
	case "key":
		return k.Name
	case "project":
		return k.Project
	}
	return b.Scope
}

// projectSpend is month-to-date spend over every key in the project (by id),
// revoked ones included, on the same basis as GET /budgets. KeyBy holds a
// rotating key under both secrets, so each key counts once.
func (s *Snapshot) projectSpend(projectID string) float64 {
	seen := map[string]bool{}
	var usd float64
	for _, k := range s.KeyBy {
		if k.ProjectID == projectID && !seen[k.ID] {
			seen[k.ID] = true
			usd += s.Spend.ByKey[k.ID]
		}
	}
	return usd
}

// Admit runs everything before the upstream call.
func Admit(s *Snapshot, in Input, r *rand.Rand) *Decision {
	k, rej := Authenticate(s, in.Secret, in.Now)
	if rej != nil {
		return &Decision{start: in.Now, requested: in.Req.Model, Req: in.Req, Reject: rej}
	}
	d := AdmitKey(s, k, in, r)
	d.Receipt.SecretID = SecretID(demo.HashSecret(strings.TrimPrefix(in.Secret, "Bearer ")))
	return d
}

// AdmitKey is Admit for a key something else already authenticated: Warden
// runs after Agent Router's key check, which removes the secret.
func AdmitKey(s *Snapshot, k *store.KeyRecord, in Input, r *rand.Rand) *Decision {
	d := &Decision{start: in.Now, requested: in.Req.Model, Req: in.Req}
	rc := &model.Receipt{
		ID: hexStr(r, 8) + "-" + hexStr(r, 4), TenantID: s.Tenant, TraceID: hexStr(r, 32),
		SessionID: in.SessionID, TS: in.Now.UnixMilli(), KeyID: k.ID, KeyName: k.Name, Team: k.Team, Project: k.Project, ProjectID: k.ProjectID,
		Actor: in.Actor, RequestedModel: in.Req.Model, RouteReason: "explicit", Verdict: "allowed", InboundVerdict: "skipped", PolicyMode: "enforced",
		Redactions: []model.Redaction{}, Rules: []model.RuleEval{}, RequestHash: sha(in.Body),
	}
	d.Receipt = rc
	d.identity = model.TraceStep{Step: "Identity resolved", Input: "Bearer " + k.Prefix + "…", Outcome: k.Name + " → " + k.Team + " / " + k.Project, MS: 0.3, State: "ok"}
	for _, m := range in.Req.Messages {
		d.promptToks += fakellm.EstimateTokens(m.Content) + 4
	}

	// Budget (§5.2): spend is month to date from the aggregates.
	d.budgetStep = model.TraceStep{Step: "Budget checked", Input: "no budget applies", Outcome: "skipped", MS: 0.1, State: "skip"}
	if b, spent, ok := s.governingBudget(k); ok {
		name := budgetName(b, k)
		d.budgetStep.Input = fmt.Sprintf("%s budget %s · %s of %s", b.ScopeType, name, money(spent), money(b.CapUSD))
		d.budgetStep.Outcome, d.budgetStep.State = "within cap", "ok"
		if spent >= b.CapUSD {
			scope := strings.ToUpper(b.ScopeType[:1]) + b.ScopeType[1:]
			switch b.OnExceed {
			case "block":
				d.budgetStep.Outcome, d.budgetStep.State = "over cap · blocked", "fail"
				return d.block(429, "budget_exceeded", fmt.Sprintf("%s budget %s is over its %s monthly cap. Ask a finance admin to raise it.",
					scope, name, money(b.CapUSD)))
			case "throttle":
				// Each key gets ThrottleRate requests a window; the next waits for a slot.
				rate := fmt.Sprintf("over cap · throttled to %d a minute per key", ThrottleRate)
				ok, used, wait := in.Throttle.Take(k.ID, in.Now)
				if !ok {
					secs := max(1, int(math.Ceil(wait.Seconds())))
					d.budgetStep.Outcome, d.budgetStep.State = fmt.Sprintf("%s · refused, next slot in %ds", rate, secs), "throttle"
					d.block(429, "budget_throttled", fmt.Sprintf("%s budget %s is over its %s monthly cap, so each key is throttled to %d requests a minute. Retry after %ds, or ask a finance admin to raise the cap.",
						scope, name, money(b.CapUSD), ThrottleRate, secs))
					d.Reject.RetryAfter = secs
					return d
				}
				d.budgetStep.Outcome, d.budgetStep.State = fmt.Sprintf("%s · admitted, %d of %d", rate, used, ThrottleRate), "warn"
			default:
				d.budgetStep.Outcome, d.budgetStep.State = "over cap · warning only", "warn"
			}
		}
	}

	resolved, aliased := store.ResolveAlias(s.Aliases, in.Req.Model)
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
	d.Vault = &Vault{}
	names := newPlaceholders(promptText(msgs))
	for _, rule := range s.Rules {
		if rule.Mode == "draft" || rule.Mode == "disabled" {
			continue
		}
		t0 := time.Now()
		if !in.Deadline.IsZero() && t0.After(in.Deadline) {
			d.pastDeadline = true
			ev := model.RuleEval{RuleID: rule.ID, Name: rule.Name, Version: rule.Version, Action: "not evaluated · deadline · fails open"}
			if rule.Mode == "enforce" && rule.FailMode == "closed" {
				ev.Action = "not evaluated · deadline · fails closed"
				rc.Rules = append(rc.Rules, ev)
				d.blockedBy = fmt.Sprintf("%s v%d not evaluated before the deadline · fails closed", rule.Name, rule.Version)
				return d.block(503, "policy_deadline", fmt.Sprintf("Rule %s v%d couldn't be evaluated in time and fails closed. Retry the request.", rule.Name, rule.Version))
			}
			rc.Rules = append(rc.Rules, ev)
			continue
		}
		ok, found := match(rule, ruleCtx{team: k.Team, project: k.ProjectID, key: k.Name, model: resolved, provider: current.Provider, region: in.Region, prompt: promptText(msgs), det: s.Detectors})
		ev := model.RuleEval{RuleID: rule.ID, Name: rule.Name, Version: rule.Version, Matched: ok, Action: "no match"}
		if ok && len(rule.Then) > 0 {
			act := rule.Then[0]
			ev.Action = act.Action
			if rule.Mode == "monitor" {
				ev.Action = "would " + act.Action
			} else {
				switch act.Action {
				case "block":
					ev.MS = round2(float64(time.Since(t0).Microseconds())/1000 + 0.1)
					rc.Rules = append(rc.Rules, ev)
					d.rulesMS += ev.MS
					ent := firstKey(found)
					if ent == "" {
						// Matched on who or where, not on content.
						d.blockedBy = fmt.Sprintf("blocked by %s v%d", rule.Name, rule.Version)
						return d.block(403, "policy_blocked", fmt.Sprintf("Rule %s v%d blocks this request.", rule.Name, rule.Version))
					}
					d.blockedBy = fmt.Sprintf("blocked by %s v%d on entity %q", rule.Name, rule.Version, ent)
					return d.block(403, "policy_blocked", fmt.Sprintf("Rule %s v%d matched entity %q. Remove it from the prompt, or route through a self-hosted backend.", rule.Name, rule.Version, ent))
				case "redact":
					back := strings.Contains(act.Detail, RehydrateOnReturn)
					for ent, n := range found {
						for i := range msgs {
							msgs[i].Content = s.Detectors.redact(ent, msgs[i].Content, func(label, m string) string {
								ph := names.For(label, m)
								if back {
									d.Vault.put(ph, m, ent)
								}
								return ph
							})
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
		for _, t := range rt.Fallback {
			if b, ok := s.backend(t.Backend); ok && !seen[t.Backend] && b.Health != "down" {
				m := t.Model
				if m == "" {
					m = s.substitute(b, resolved)
				}
				d.Candidates = append(d.Candidates, Candidate{b, m})
				seen[t.Backend] = true
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

// verdict is an admitted request's outbound verdict.
func (d *Decision) verdict() string {
	switch {
	case len(d.Receipt.Redactions) > 0:
		return "redacted"
	case d.rerouted:
		return "rerouted"
	}
	return "allowed"
}

// Rerouted reports whether a rule changed the model or backend.
func (d *Decision) Rerouted() bool { return d.rerouted }

// Policy is the part of a receipt the policy engine decides, for a gateway
// that logs the rest itself: Warden sends it along with each request, and
// receipt-ingest lays it over the access-log record (spec §4.6: Warden owns
// verdicts, rules and redactions; the gateway owns timing, tokens, upstream).
type Policy struct {
	// Mode is how Warden handled the request: "enforced", "passthrough" (kill
	// switch), or "fail-open"/"fail-closed" when it couldn't decide in time.
	Mode           string            `json:"mode"`
	Verdict        string            `json:"verdict"`
	RequestedModel string            `json:"requestedModel,omitempty"`
	RouteReason    string            `json:"routeReason,omitempty"`
	Rules          []model.RuleEval  `json:"rules"`
	Redactions     []model.Redaction `json:"redactions"`
	RequestHash    string            `json:"requestHash,omitempty"`
	Actor          string            `json:"actor,omitempty"`
	SessionID      string            `json:"sessionId,omitempty"`
	Trace          []model.TraceStep `json:"trace"` // the steps after identity
	Blocked        *PolicyBlock      `json:"blocked,omitempty"`
}

// PolicyBlock is the rest of a receipt for a request the engine refused: it
// never reached a backend, so the access log has nothing to add.
type PolicyBlock struct {
	Status        int    `json:"status"`
	ErrorCode     string `json:"errorCode"`
	ErrorDetail   string `json:"errorDetail"`
	ResolvedModel string `json:"resolvedModel"`
	Backend       string `json:"backend"`
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	InputTokens   int    `json:"inputTokens"`
}

// Policy summarizes the decision. For a refused request it finishes the
// receipt; an admitted one's upstream outcome is someone else's to record.
func (d *Decision) Policy(s *Snapshot, now time.Time) Policy {
	rc := d.Receipt
	mode := "enforced"
	if d.pastDeadline {
		mode = "fail-open"
		if d.Reject != nil {
			mode = "fail-closed"
		}
	}
	p := Policy{Mode: mode, RequestedModel: d.requested, RouteReason: rc.RouteReason, Rules: rc.Rules,
		Redactions: rc.Redactions, RequestHash: rc.RequestHash, Actor: rc.Actor, SessionID: rc.SessionID}
	if d.Reject != nil {
		f := d.Finish(s, nil, Result{}, nil, now)
		p.Verdict, p.Trace = f.Verdict, f.Trace[1:]
		p.Blocked = &PolicyBlock{Status: f.Status, ErrorCode: f.ErrorCode, ErrorDetail: f.ErrorDetail, ResolvedModel: f.ResolvedModel,
			Backend: f.Backend, Provider: f.Provider, Region: f.Region, InputTokens: f.InputTokens}
		return p
	}
	p.Verdict, p.Trace = d.verdict(), []model.TraceStep{d.budgetStep, d.rulesStep}
	return p
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

	rc.CostUSD = new(float64) // nothing billed unless served
	if d.Reject != nil {
		rc.Verdict, rc.Status, rc.ErrorCode, rc.ErrorDetail = "blocked", d.Reject.Status, d.Reject.Code, d.Reject.Message
		if d.Reject.Throttled() {
			rc.Verdict = "throttled"
		}
		rc.ResolvedModel = d.Req.Model
		if r, ok := store.ResolveAlias(s.Aliases, d.Req.Model); ok {
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
		} else if d.budgetStep.State != "fail" && d.budgetStep.State != "throttle" {
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
		rc.CacheWriteTokens = res.Usage.PromptTokensDetails.CacheCreationTokens
	} else if res.Status == 200 {
		rc.OutputTokens = fakellm.EstimateTokens(res.Content) // stream cut before usage arrived
	}
	if res.Status == 200 {
		rc.CostUSD, rc.CostBasis = s.Cost(m, b.Name, d.start, TokensOf(rc))
	} else {
		rc.ErrorCode = "upstream_error"
		if res.Status == 429 {
			rc.ErrorCode = "upstream_rate_limited"
		}
		rc.ErrorDetail = fmt.Sprintf("%s returned %d: %s", b.Name, res.Status, res.ErrMsg)
	}
	rc.ResponseHash = sha([]byte(res.Content))

	rc.Verdict = d.verdict()
	if res.Status == 200 {
		rc.InboundVerdict = "allowed" // inspected, clean
	}
	if res.Truncated {
		rc.Verdict, rc.InboundVerdict = "truncated", "blocked"
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
