// Package model holds the wire types the console consumes. JSON field names
// match console/src/data/mock.ts exactly so the UI can swap data sources
// without reshaping anything.
package model

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Project groups a team's keys: here, a team's model resource. Receipts
// name the project by id.
type Project struct {
	ID   string `json:"id"`
	Team string `json:"team"`
	Name string `json:"name"`
}

// Model is a model seen in the usage, and the provider that prices it.
type Model struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

type APIKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Team       string `json:"team"`
	Project    string `json:"project"` // the project's name, which receipts carry
	ProjectID  string `json:"projectId"`
	LastUsedAt *int64 `json:"lastUsedAt"` // epoch ms; the console formats it
	// Requests24h, Spend24hUSD and Unpriced24h cover the rolling 24h;
	// Spend24hUSD leaves out the Unpriced24h requests, which have no price.
	Requests24h int     `json:"requests24h"`
	Spend24hUSD float64 `json:"spend24hUsd"`
	Unpriced24h int     `json:"unpriced24h"`
	// Hourly24h is requests per hour over the same rolling 24h, oldest first.
	Hourly24h []int `json:"hourly24h"`
	// Owner is the person whose requests the key carries.
	Owner string `json:"owner"`
}

type Receipt struct {
	ID                string   `json:"id"`
	TraceID           string   `json:"traceId"`
	TS                int64    `json:"ts"` // epoch ms
	DurationMS        int      `json:"durationMs"`
	KeyID             string   `json:"keyId"`
	KeyName           string   `json:"keyName"`
	Team              string   `json:"team"`
	Project           string   `json:"project"` // the project's name when the request was made
	ProjectID         string   `json:"projectId,omitempty"`
	Actor             string   `json:"actor,omitempty"`
	RequestedModel    string   `json:"requestedModel"`
	ResolvedModel     string   `json:"resolvedModel"`
	Backend           string   `json:"backend"`
	InputTokens       int      `json:"inputTokens"`
	CachedInputTokens int      `json:"cachedInputTokens"`
	OutputTokens      int      `json:"outputTokens"`
	ReasoningTokens   int      `json:"reasoningTokens"`
	CacheWriteTokens  int      `json:"cacheWriteTokens"` // input tokens written to the provider's prompt cache
	CostUSD           *float64 `json:"costUsd"`          // nil when the model had no price: unknown, not $0
	Verdict           string   `json:"verdict"`          // allowed | blocked | throttled | truncated
	Status            int      `json:"status"`
	ErrorCode         string   `json:"errorCode,omitempty"`
	ErrorDetail       string   `json:"errorDetail,omitempty"`
	InFlight          bool     `json:"inFlight,omitempty"`
}

// SeriesPoint is one bucket of the traffic chart, by verdict.
type SeriesPoint struct {
	T         int64 `json:"t"`
	Allowed   int   `json:"allowed"`
	Blocked   int   `json:"blocked"`
	Truncated int   `json:"truncated"`
	Throttled int   `json:"throttled"`
}

type SpendPoint struct {
	Day    string             `json:"day"` // MM-DD, UTC
	ByTeam map[string]float64 `json:"byTeam"`
}
