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

// Model is a catalog entry. Prices are per (model, backend).
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
