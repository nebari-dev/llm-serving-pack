// Package model holds the wire types the console consumes. JSON field names
// match console/src/data/mock.ts exactly so the UI can swap data sources
// without reshaping anything.
package model

type Team struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CostCenter string `json:"costCenter"`
}

type Model struct {
	ID            string  `json:"id"`
	Display       string  `json:"display"`
	Provider      string  `json:"provider"`
	Family        string  `json:"family"`
	Context       int     `json:"context"`
	InPerM        float64 `json:"inPerM"`
	OutPerM       float64 `json:"outPerM"`
	CachedPerM    float64 `json:"cachedPerM"`
	ReasoningPerM float64 `json:"reasoningPerM"`
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
	CaptureContent bool     `json:"captureContent,omitempty"`
}

type RouteTarget struct {
	Model   string `json:"model"`
	Backend string `json:"backend"`
	Weight  int    `json:"weight"`
}

type Route struct {
	Name           string        `json:"name"`
	Match          string        `json:"match"`
	Targets        []RouteTarget `json:"targets"`
	Fallback       []string      `json:"fallback"`
	Provenance     string        `json:"provenance"`
	Sync           string        `json:"sync"`
	CaptureContent bool          `json:"captureContent,omitempty"`
}

type APIKey struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Prefix         string   `json:"prefix"`
	Team           string   `json:"team"`
	Project        string   `json:"project"`
	AllowedModels  []string `json:"allowedModels"`
	AllowedRegions []string `json:"allowedRegions"`
	BudgetID       string   `json:"budgetId,omitempty"`
	ExpiresAt      *string  `json:"expiresAt"`
	LastUsedAt     *int64   `json:"lastUsedAt"` // epoch ms; the console formats it
	Requests24h    int      `json:"requests24h"`
	Status         string   `json:"status"`
}

type Budget struct {
	ID           string  `json:"id"`
	Scope        string  `json:"scope"`
	ScopeType    string  `json:"scopeType"`
	Period       string  `json:"period"`
	CapUSD       float64 `json:"capUsd"`
	CurrentUSD   float64 `json:"currentUsd"`
	OnExceed     string  `json:"onExceed"`
	ProjectedUSD float64 `json:"projectedUsd"`
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

type PolicyRule struct {
	ID          string   `json:"id"`
	Ordinal     int      `json:"ordinal"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Mode        string   `json:"mode"`
	FailMode    string   `json:"failMode"`
	Version     int      `json:"version"`
	When        []Cond   `json:"when"`
	Then        []Action `json:"then"`
	Fired24h    int      `json:"fired24h"`
	Baseline7d  int      `json:"baseline7d"`
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
	State   string  `json:"state"` // ok | warn | fail | skip
}

type RuleEval struct {
	RuleID  string  `json:"ruleId"`
	Name    string  `json:"name"`
	Version int     `json:"version"`
	Matched bool    `json:"matched"`
	Action  string  `json:"action"`
	MS      float64 `json:"ms"`
}

type Redaction struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type Receipt struct {
	ID                string      `json:"id"`
	TenantID          string      `json:"-"`
	TraceID           string      `json:"traceId"`
	SessionID         string      `json:"sessionId,omitempty"`
	TS                int64       `json:"ts"` // epoch ms
	DurationMS        int         `json:"durationMs"`
	TTFTMS            *int        `json:"ttftMs,omitempty"`
	KeyID             string      `json:"keyId"`
	KeyName           string      `json:"keyName"`
	Team              string      `json:"team"`
	Project           string      `json:"project"`
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
	CostUSD           float64     `json:"costUsd"`
	CostBasis         *Model      `json:"-"`
	Verdict           string      `json:"verdict"`
	InboundVerdict    string      `json:"inboundVerdict"`
	Redactions        []Redaction `json:"redactions"`
	Rules             []RuleEval  `json:"rules"`
	Status            int         `json:"status"`
	ErrorCode         string      `json:"errorCode,omitempty"`
	ErrorDetail       string      `json:"errorDetail,omitempty"`
	RequestHash       string      `json:"requestHash"`
	ResponseHash      string      `json:"responseHash"`
	ContentCaptured   bool        `json:"contentCaptured"`
	Content           any         `json:"-"`
	InFlight          bool        `json:"inFlight,omitempty"`
	Trace             []TraceStep `json:"trace"`
}

type SeriesPoint struct {
	T         int64 `json:"t"`
	Allowed   int   `json:"allowed"`
	Redacted  int   `json:"redacted"`
	Rerouted  int   `json:"rerouted"`
	Blocked   int   `json:"blocked"`
	Truncated int   `json:"truncated"`
}

type SpendPoint struct {
	Day    string             `json:"day"` // MM-DD, UTC
	ByTeam map[string]float64 `json:"byTeam"`
}
