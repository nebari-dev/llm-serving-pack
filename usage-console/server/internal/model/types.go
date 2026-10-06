// Package model holds the wire types the console consumes. JSON field names
// match console/src/data/mock.ts exactly so the UI can swap data sources
// without reshaping anything.
package model

type Team struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CostCenter string `json:"costCenter"`
}

// Project groups a team's keys (§5.2). Names are for people and unique
// within a team (ignoring case); receipts, budgets and rules name the
// project by id, so it can be renamed.
type Project struct {
	ID   string `json:"id"`
	Team string `json:"team"`
	Name string `json:"name"`
	// Deleted projects stay, so the history of their revoked keys keeps a
	// name; GET /projects leaves them out.
	Deleted bool   `json:"-"`
	ETag    string `json:"etag,omitempty"` // for If-Match
}

// Model is a catalog entry. Prices are per (model, backend): see Pricing.
type Model struct {
	ID       string `json:"id"`
	Display  string `json:"display"`
	Provider string `json:"provider"`
	Family   string `json:"family"`
	Context  int    `json:"context"`
	// From LiteLLM's file for the backends serving it; nil when none has an entry.
	Modalities   []string      `json:"modalities,omitempty"`
	Deprecations []Deprecation `json:"deprecations,omitempty"`
}

// Deprecation is the date a backend's provider retires a model.
type Deprecation struct {
	Backend string `json:"backend"`
	Date    string `json:"date"` // YYYY-MM-DD
}

// CostBasis is the price a receipt was costed with (§5.1). Receipts from
// before per-backend prices carry only the model and its four rates; the
// field names are theirs, so both decode.
type CostBasis struct {
	ID       string `json:"id"`
	Display  string `json:"display"`
	Provider string `json:"provider"`
	Family   string `json:"family"`
	Context  int    `json:"context"`
	Backend  string `json:"backend,omitempty"`
	// EffectiveFrom is when the price row took effect (epoch ms).
	EffectiveFrom  int64    `json:"effectiveFrom,omitempty"`
	InPerM         *float64 `json:"inPerM"`
	CachedPerM     *float64 `json:"cachedPerM"`
	CacheWritePerM *float64 `json:"cacheWritePerM,omitempty"`
	OutPerM        *float64 `json:"outPerM"`
	ReasoningPerM  *float64 `json:"reasoningPerM"`
	// Sources say where each rate came from (seed | litellm | manual), keyed
	// input, cachedInput, cacheWrite, output, reasoning.
	Sources map[string]string `json:"sources,omitempty"`
	// PricedLater marks a receipt that had no price when it settled and was
	// priced once its (model, backend) got one.
	PricedLater bool `json:"pricedLater,omitempty"`
}

// Alias is a model_aliases row with the requests that matched it over the
// rolling 24h.
type Alias struct {
	Alias       string `json:"alias"`
	Target      string `json:"target"`
	Requests24h int    `json:"requests24h"`
	ETag        string `json:"etag"` // for If-Match
}

// Pricing is every (model, backend) the tenant's backends serve with the
// price in effect now, every rate change between consecutive rows (newest
// first), the LiteLLM proposals waiting on a decision, and the sync's state.
type Pricing struct {
	Prices    []PairPrice     `json:"prices"`
	Changes   []PriceChange   `json:"changes"`
	Proposals []PriceProposal `json:"proposals"`
	Sync      PriceSync       `json:"sync"`
}

// PairPrice is a (model, backend)'s price in effect now. Rates are keyed
// input, cachedInput, cacheWrite, output, reasoning; a missing key has no
// price. Priced is all five set.
type PairPrice struct {
	Model      string `json:"model"`
	Backend    string `json:"backend"`
	LiteLLMKey string `json:"litellmKey,omitempty"`
	// EffectiveFrom is the day the row in effect started; "" with no price.
	EffectiveFrom string                `json:"effectiveFrom,omitempty"`
	Rates         map[string]*PriceRate `json:"rates"`
	Priced        bool                  `json:"priced"`
	// LiteLLM is the last value LiteLLM gave each rate, which an override
	// would go back to following.
	LiteLLM map[string]float64 `json:"litellm,omitempty"`
	ETag    string             `json:"etag"` // for If-Match
}

type PriceRate struct {
	PerM   float64 `json:"perM"`
	Source string  `json:"source"` // seed | litellm | manual
}

// PriceChange is one rate moving between consecutive rows of a pair. From
// or To is nil where the pair had no price.
type PriceChange struct {
	Model   string   `json:"model"`
	Backend string   `json:"backend"`
	Field   string   `json:"field"`
	From    *float64 `json:"from"`
	To      *float64 `json:"to"`
	// Source is where the new rate came from; "" when it ended.
	Source    string `json:"source,omitempty"`
	Effective string `json:"effective"`
	// EffectiveAt is the exact start (epoch ms), which names the row to cancel
	// while it's still scheduled.
	EffectiveAt int64 `json:"effectiveAt"`
	Scheduled   bool  `json:"scheduled"`
}

// PriceProposal is a LiteLLM move on an overridden rate.
type PriceProposal struct {
	ID         int64   `json:"id"`
	Model      string  `json:"model"`
	Backend    string  `json:"backend"`
	Rate       string  `json:"rate"`
	Current    float64 `json:"current"`
	Proposed   float64 `json:"proposed"`
	LiteLLMKey string  `json:"litellmKey"`
	CreatedAt  int64   `json:"createdAt"` // epoch ms
}

// PriceSync is the LiteLLM sync's state. Times are epoch ms; 0 is never.
type PriceSync struct {
	Source    string `json:"source"` // the file's URL
	LastRunAt int64  `json:"lastRunAt"`
	LastOKAt  int64  `json:"lastOkAt"`
	Error     string `json:"error,omitempty"` // the last run's, if it failed
	Applied   int    `json:"applied"`
	Proposed  int    `json:"proposed"`
	Retired   int    `json:"retired"`
	NextRunAt int64  `json:"nextRunAt"`
}

type Backend struct {
	Name           string   `json:"name"`
	Provider       string   `json:"provider"`
	Region         string   `json:"region"`
	Provenance     string   `json:"provenance"`
	Sync           string   `json:"sync"`
	Source         string   `json:"source,omitempty"`
	Models         []string `json:"models"`
	Health         string   `json:"health"`
	P50            int      `json:"p50"`
	ErrorRate      float64  `json:"errorRate"`
	Requests1h     int      `json:"requests1h"`
	CaptureContent bool     `json:"captureContent,omitempty"`
	// Endpoint is where the gateway reaches the backend; nil for one it
	// can't (a seeded backend with no endpoint is never compiled).
	Endpoint *BackendEndpoint `json:"endpoint,omitempty"`
	// YAML is the gateway resources the backend compiles to (API only).
	YAML string `json:"yaml,omitempty"`
	// Key is what the control plane may say about the provider key: never
	// the key itself (§9.1). Nil when none was set from the console.
	Key *ProviderKey `json:"key,omitempty"`
	// LastTest is the last connection test of the saved backend.
	LastTest *BackendTest `json:"lastTest,omitempty"`
	ETag     string       `json:"etag,omitempty"` // for If-Match
}

// ProviderKey is a provider key as the console sees it: its non-secret
// prefix and when it was set.
type ProviderKey struct {
	Prefix string `json:"prefix"`
	SetAt  int64  `json:"setAt"` // epoch ms
}

// BackendTest is a connection test: OK with the models the provider listed,
// or not, with the provider's refusal.
type BackendTest struct {
	At      int64  `json:"at"` // epoch ms
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// BackendEndpoint is a backend's desired state: Schema and Prefix for the
// AIServiceBackend, Host and Port (either may be an aigw ${VAR:-default}) for
// the Backend, and APIKeyEnv, if set, the environment variable holding the
// provider key the gateway sends.
type BackendEndpoint struct {
	Schema    string `json:"schema"`
	Prefix    string `json:"prefix"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	TLS       bool   `json:"tls,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	// BaseURL is the endpoint as a URL, ${VAR:-default}s at their defaults
	// (API only).
	BaseURL string `json:"baseUrl,omitempty"`
	// KeyVersion is when the console last set the key, which the compiled
	// Secret carries so a replaced key shows as a change to apply.
	KeyVersion string `json:"-"`
}

// RouteTarget is a backend a route sends to: Model, if set, replaces the
// requested model (modelNameOverride), and Weight splits traffic between a
// route's targets.
type RouteTarget struct {
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	Weight  int    `json:"weight,omitempty"`
}

// RouteMatch is which requests a route takes: any of Models (exact names, or
// a single "prefix*" or "*"), and every one of Headers.
type RouteMatch struct {
	Models  []string      `json:"models"`
	Headers []HeaderMatch `json:"headers"`
}

type HeaderMatch struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Route is desired routing state (spec §4.4): it compiles to one rule of the
// gateway's AIGatewayRoute. Fallback is tried in order when the targets fail.
type Route struct {
	Name           string        `json:"name"`
	Match          RouteMatch    `json:"match"`
	Targets        []RouteTarget `json:"targets"`
	Fallback       []RouteTarget `json:"fallback"`
	CaptureContent bool          `json:"captureContent,omitempty"`
	// Sync is synced when the gateway runs this route as it is, pending when
	// it doesn't yet, failed when the last apply didn't take.
	Sync string `json:"sync"`
	ETag string `json:"etag,omitempty"`
}

type APIKey struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Prefix         string   `json:"prefix"`
	Team           string   `json:"team"`
	Project        string   `json:"project"` // the project's name, which receipts carry
	ProjectID      string   `json:"projectId"`
	AllowedModels  []string `json:"allowedModels"`
	AllowedRegions []string `json:"allowedRegions"`
	ExpiresAt      *string  `json:"expiresAt"`
	LastUsedAt     *int64   `json:"lastUsedAt"` // epoch ms; the console formats it
	Requests24h    int      `json:"requests24h"`
	Spend24hUSD    float64  `json:"spend24hUsd"`
	// Unpriced24h is requests over the same 24h with no price, which
	// Spend24hUSD leaves out.
	Unpriced24h int `json:"unpriced24h"`
	// Hourly24h is requests per hour over the same rolling 24h, oldest first.
	Hourly24h []int        `json:"hourly24h"`
	Status    string       `json:"status"`
	Rotation  *KeyRotation `json:"rotation,omitempty"`
	// Owner may revoke, rotate, extend and finish the key; anyone else needs
	// admin (backend-decisions §7). Whoever created it.
	Owner string `json:"owner"`
}

// KeyRotation is a rotating key's overlap window. Each field is null when
// it wasn't recorded.
type KeyRotation struct {
	StartedAt *int64  `json:"startedAt"` // epoch ms, from the audit log
	StartedBy *string `json:"startedBy"`
	EndsAt    *int64  `json:"endsAt"` // epoch ms, from rotate_until
	// Requests since the rotation started, by the secret that authenticated
	// them; null without a start. Unrecorded is requests from before
	// receipts recorded the secret.
	OldSecretRequests  *int `json:"oldSecretRequests"`
	NewSecretRequests  *int `json:"newSecretRequests"`
	UnrecordedRequests int  `json:"unrecordedRequests"`
}

type Budget struct {
	ID string `json:"id"`
	// Scope is a team id, a project id or a key id.
	Scope string `json:"scope"`
	// ScopeName is what to show for it: the team id, the project's or the
	// key's name (the id when the key is gone).
	ScopeName  string  `json:"scopeName,omitempty"`
	ScopeType  string  `json:"scopeType"`
	Period     string  `json:"period"`
	CapUSD     float64 `json:"capUsd"`
	CurrentUSD float64 `json:"currentUsd"`
	OnExceed   string  `json:"onExceed"`
	// ThrottlePerMinute is how many requests a minute each covered key gets
	// while a throttle budget is over its cap (gateway.ThrottleRate).
	ThrottlePerMinute int     `json:"throttlePerMinute,omitempty"`
	ProjectedUSD      float64 `json:"projectedUsd"`
	// TrailingDailyUSD is the scope's daily average the projection uses.
	TrailingDailyUSD float64 `json:"trailingDailyUsd"`
	// UnpricedRequests is this month's requests in scope with no price:
	// not in CurrentUSD, and not counted against the cap.
	UnpricedRequests int    `json:"unpricedRequests"`
	ETag             string `json:"etag,omitempty"` // for If-Match
}

type Cond struct {
	Field string   `json:"field"`
	Op    string   `json:"op"`
	Value []string `json:"value"`
}

type Action struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// Policy is a named, ordered list of rules (spec §5.2), versioned, published
// and rolled back as a unit. Mode is the policy's status: "draft" (never
// published), "enforce" or "monitor" (§5.2's active), or "disabled". Fail
// mode is the policy's (§4.5). Policies evaluate in Ordinal order, and each
// one's rules in list order (§5.3).
type Policy struct {
	ID          string       `json:"id"`
	Ordinal     int          `json:"ordinal"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Mode        string       `json:"mode"`
	FailMode    string       `json:"failMode"`
	Version     int          `json:"version"`
	Rules       []PolicyRule `json:"rules"`
	Fired24h    int          `json:"fired24h"`
	Baseline7d  int          `json:"baseline7d"`
}

// PolicyRule is one rule of a policy: conditions that must all hold, and
// the actions taken when they do (§5.3).
type PolicyRule struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	When []Cond   `json:"when"`
	Then []Action `json:"then"`
}

type Detector struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Threshold float64 `json:"threshold"`
	Hits24h   int     `json:"hits24h"`
	FP        int     `json:"fp"`
}

type Change struct {
	ID         string `json:"id"`
	TS         int64  `json:"ts"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	TargetKind string `json:"targetKind"`
	Effect     string `json:"effect,omitempty"`
	EffectTone string `json:"effectTone,omitempty"`
	Source     string `json:"source"`
}

type TraceStep struct {
	Step    string  `json:"step"`
	Input   string  `json:"input"`
	Outcome string  `json:"outcome"`
	MS      float64 `json:"ms"`
	State   string  `json:"state"` // ok | warn | fail | throttle | skip
}

// RuleEval is one rule's evaluation; Version is its policy's. Receipts from
// before policies carry no policy fields, and their rule id is the policy's
// id (config migration 045 made each rule a policy with the same id).
type RuleEval struct {
	RuleID   string  `json:"ruleId"`
	Name     string  `json:"name"`
	PolicyID string  `json:"policyId,omitempty"`
	Policy   string  `json:"policy,omitempty"`
	Version  int     `json:"version"`
	Matched  bool    `json:"matched"`
	Action   string  `json:"action"`
	MS       float64 `json:"ms"`
}

type Redaction struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
	// Rehydrated is how many placeholders of this type the response had put
	// back (a rule that rehydrates on return, §4.5 step 5).
	Rehydrated int `json:"rehydrated,omitempty"`
}

type Receipt struct {
	ID         string `json:"id"`
	TenantID   string `json:"-"`
	TraceID    string `json:"traceId"`
	SessionID  string `json:"sessionId,omitempty"`
	TS         int64  `json:"ts"` // epoch ms
	DurationMS int    `json:"durationMs"`
	TTFTMS     *int   `json:"ttftMs,omitempty"`
	OverheadUS *int   `json:"overheadUs,omitempty"` // the gateway's own time before the upstream call
	KeyID      string `json:"keyId"`
	KeyName    string `json:"keyName"`
	// SecretID is which of the key's secrets authenticated the request (the
	// first 12 hex of its hash); empty when not recorded.
	SecretID          string      `json:"secretId,omitempty"`
	Team              string      `json:"team"`
	Project           string      `json:"project"` // the project's name when the request was made
	ProjectID         string      `json:"projectId,omitempty"`
	Actor             string      `json:"actor,omitempty"`
	RequestedModel    string      `json:"requestedModel"`
	ResolvedModel     string      `json:"resolvedModel"`
	Backend           string      `json:"backend"`
	Provider          string      `json:"provider"`
	Region            string      `json:"region"`
	RouteReason       string      `json:"routeReason"`
	FallbackFrom      string      `json:"fallbackFrom,omitempty"`
	InputTokens       int         `json:"inputTokens"`
	CachedInputTokens int         `json:"cachedInputTokens"`
	OutputTokens      int         `json:"outputTokens"`
	ReasoningTokens   int         `json:"reasoningTokens"`
	CacheWriteTokens  int         `json:"cacheWriteTokens"`    // input tokens written to the provider's prompt cache
	CostUSD           *float64    `json:"costUsd"`             // nil when the (model, backend) had no price: unknown, not $0
	CostBasis         *CostBasis  `json:"costBasis,omitempty"` // the price row this receipt was costed with (§5.1)
	Verdict           string      `json:"verdict"`
	InboundVerdict    string      `json:"inboundVerdict"`       // allowed | stripped | blocked | skipped (not inspected)
	PolicyMode        string      `json:"policyMode,omitempty"` // enforced | passthrough | fail-open | fail-closed; "" when nothing evaluated policy
	Redactions        []Redaction `json:"redactions"`
	Rules             []RuleEval  `json:"rules"`
	Status            int         `json:"status"`
	ErrorCode         string      `json:"errorCode,omitempty"`
	ErrorDetail       string      `json:"errorDetail,omitempty"`
	RequestHash       string      `json:"requestHash"`
	ResponseHash      string      `json:"responseHash"`
	// DataRegion is the request's x-data-region header, "" if none or not recorded.
	DataRegion      string      `json:"dataRegion,omitempty"`
	ContentCaptured bool        `json:"contentCaptured"`
	Content         any         `json:"-"`
	InFlight        bool        `json:"inFlight,omitempty"`
	Trace           []TraceStep `json:"trace"`
}

type SeriesPoint struct {
	T         int64 `json:"t"`
	Allowed   int   `json:"allowed"`
	Redacted  int   `json:"redacted"`
	Rerouted  int   `json:"rerouted"`
	Blocked   int   `json:"blocked"`
	Truncated int   `json:"truncated"`
	Throttled int   `json:"throttled"`
}

// Add counts n requests of verdict v; an unknown verdict isn't charted.
func (p *SeriesPoint) Add(v string, n int) {
	switch v {
	case "allowed":
		p.Allowed += n
	case "redacted":
		p.Redacted += n
	case "rerouted":
		p.Rerouted += n
	case "blocked":
		p.Blocked += n
	case "truncated":
		p.Truncated += n
	case "throttled":
		p.Throttled += n
	}
}

type SpendPoint struct {
	Day    string             `json:"day"` // MM-DD, UTC
	ByTeam map[string]float64 `json:"byTeam"`
}
