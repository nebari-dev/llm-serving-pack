// Package demo is the seeded demo tenant (spec §7.5.1), ported from
// console/src/data/mock.ts so the API serves the same catalog the mockup drew.
package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
)

const Tenant = "demo"

// DevSecret is the deterministic secret for a seeded key. The seed stores only
// its hash; the traffic generator derives the same secret to authenticate.
// Never used outside the demo tenant.
func DevSecret(prefix string) string { return prefix + "_devsecret_not_for_production" }

func HashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var Teams = []model.Team{
	{ID: "support", Name: "Support", CostCenter: "CC-4100"},
	{ID: "agents", Name: "Agents platform", CostCenter: "CC-4230"},
	{ID: "batch", Name: "Data batch", CostCenter: "CC-5010"},
	{ID: "web", Name: "Web app", CostCenter: "CC-4100"},
	{ID: "research", Name: "Research", CostCenter: "CC-7000"},
	{ID: "security", Name: "Security", CostCenter: "CC-9100"},
}

var Models = []model.Model{
	{ID: "gpt-5-mini", Display: "GPT-5 mini", Provider: "OpenAI", Family: "gpt-5", Context: 400_000},
	{ID: "gpt-5.5", Display: "GPT-5.5", Provider: "OpenAI", Family: "gpt-5", Context: 400_000},
	{ID: "claude-sonnet-5", Display: "Claude Sonnet 5", Provider: "Anthropic", Family: "claude", Context: 1_000_000},
	{ID: "claude-opus-4-1", Display: "Claude Opus 4.1", Provider: "Anthropic", Family: "claude", Context: 200_000},
	{ID: "claude-haiku-4-5", Display: "Claude Haiku 4.5", Provider: "Bedrock", Family: "claude", Context: 200_000},
	{ID: "llama-3.3-70b", Display: "Llama 3.3 70B", Provider: "Self-hosted", Family: "llama", Context: 128_000},
}

// seedRates are the demo's starting prices per 1M tokens: input, cached
// input, output, reasoning. Every backend serving a model starts at them,
// with cache writes at the input rate; the first LiteLLM sync replaces them.
var seedRates = map[string][4]float64{
	"gpt-5-mini":       {0.25, 0.025, 2, 2},
	"gpt-5.5":          {1.25, 0.125, 10, 10},
	"claude-sonnet-5":  {3, 0.3, 15, 15},
	"claude-opus-4-1":  {15, 1.5, 75, 75},
	"claude-haiku-4-5": {1, 0.1, 5, 5},
	"llama-3.3-70b":    {0.12, 0.12, 0.3, 0.3},
}

// SeedPrice is one (model, backend)'s starting price row.
type SeedPrice struct {
	ModelID, Backend string
	Rates            pricing.Rates
	Sources          pricing.Sources
	From             time.Time
}

// SeedPrices are the starting rows, effective from.
func SeedPrices(from time.Time) []SeedPrice {
	var out []SeedPrice
	for _, b := range Backends {
		for _, m := range b.Models {
			r := seedRates[m]
			vals := [pricing.NumRates]float64{r[0], r[1], r[0], r[2], r[3]}
			p := SeedPrice{ModelID: m, Backend: b.Name, From: from}
			for i := range vals {
				p.Rates[i], p.Sources[i] = &vals[i], pricing.Seed
			}
			out = append(out, p)
		}
	}
	return out
}

// LiteLLMKeys map each (model, backend) to its entry in LiteLLM's price
// file. claude-opus-4-1 on anthropic-prod has none: LiteLLM dropped the
// direct Anthropic entry, and borrowing a Bedrock one is a call to make in
// the console. vllm-internal is self-hosted, so has no list price.
var LiteLLMKeys = map[[2]string]string{
	{"gpt-5-mini", "openai-prod"}:         "gpt-5-mini",
	{"gpt-5.5", "openai-prod"}:            "gpt-5.5",
	{"claude-sonnet-5", "anthropic-prod"}: "claude-sonnet-5",
	{"claude-haiku-4-5", "bedrock-eu"}:    "eu.anthropic.claude-haiku-4-5-20251001-v1:0",
	{"claude-sonnet-5", "bedrock-eu"}:     "eu.anthropic.claude-sonnet-5",
	{"gpt-5-mini", "azure-openai-eu"}:     "azure/eu/gpt-5-mini-2025-08-07",
}

// Aliases resolve before routing. A trailing * is a prefix match.
var Aliases = map[string]string{"summarize-*": "gpt-5-mini"}

var Backends = []model.Backend{
	{Name: "openai-prod", Provider: "OpenAI", Region: "us-east", Provenance: "console", Sync: "synced", Models: []string{"gpt-5-mini", "gpt-5.5"}, Health: "healthy", P50: 412, ErrorRate: 0.2},
	{Name: "anthropic-prod", Provider: "Anthropic", Region: "us-east", Provenance: "console", Sync: "applying", Models: []string{"claude-sonnet-5", "claude-opus-4-1"}, Health: "degraded", P50: 980, ErrorRate: 3.1},
	{Name: "bedrock-eu", Provider: "Bedrock", Region: "eu-central", Provenance: "git", Sync: "synced", Source: "github.com/acme/platform-gitops/blob/main/gateway/backends/bedrock-eu.yaml", Models: []string{"claude-haiku-4-5", "claude-sonnet-5"}, Health: "healthy", P50: 640, ErrorRate: 0.4},
	{Name: "vllm-internal", Provider: "Self-hosted", Region: "eu-private", Provenance: "git", Sync: "drift", Source: "github.com/acme/platform-gitops/blob/main/gateway/backends/vllm-internal.yaml", Models: []string{"llama-3.3-70b"}, Health: "healthy", P50: 220, ErrorRate: 0.1, CaptureContent: true},
	{Name: "azure-openai-eu", Provider: "Azure", Region: "eu-west", Provenance: "adopted", Sync: "failed", Models: []string{"gpt-5-mini"}, Health: "down", P50: 0, ErrorRate: 100},
}

var Routes = []model.Route{
	{Name: "default", Match: "model = *", Targets: []model.RouteTarget{{Model: "claude-sonnet-5", Backend: "anthropic-prod", Weight: 100}}, Fallback: []string{"bedrock-eu", "openai-prod"}, Provenance: "console", Sync: "synced"},
	{Name: "cheap-summarize", Match: "model = summarize-*", Targets: []model.RouteTarget{{Model: "gpt-5-mini", Backend: "openai-prod", Weight: 100}}, Fallback: []string{"vllm-internal"}, Provenance: "console", Sync: "synced"},
	{Name: "eu-private", Match: "header x-data-region = eu", Targets: []model.RouteTarget{{Model: "llama-3.3-70b", Backend: "vllm-internal", Weight: 80}, {Model: "claude-haiku-4-5", Backend: "bedrock-eu", Weight: 20}}, Fallback: []string{}, Provenance: "git", Sync: "drift", CaptureContent: true},
	{Name: "research-frontier", Match: "key.team = research", Targets: []model.RouteTarget{{Model: "claude-opus-4-1", Backend: "anthropic-prod", Weight: 100}}, Fallback: []string{"openai-prod"}, Provenance: "console", Sync: "applying"},
}

func date(s string) *string { return &s }

var Keys = []model.APIKey{
	{ID: "k1", Name: "support-bot", Prefix: "ngw_live_7f3a", Team: "support", Project: "helpdesk", AllowedModels: []string{"gpt-5-mini", "claude-sonnet-5"}, AllowedRegions: []string{"us-east", "eu-central"}, ExpiresAt: date("2027-03-01"), Status: "active"},
	{ID: "k2", Name: "agents-prod", Prefix: "ngw_live_c19e", Team: "agents", Project: "orchestrator", AllowedModels: []string{"claude-sonnet-5", "claude-opus-4-1", "gpt-5.5"}, AllowedRegions: []string{"us-east"}, ExpiresAt: date("2026-12-31"), Status: "active"},
	{ID: "k3", Name: "batch-summarize", Prefix: "ngw_live_02bd", Team: "batch", Project: "nightly-digest", AllowedModels: []string{"gpt-5-mini", "claude-opus-4-1", "llama-3.3-70b"}, AllowedRegions: []string{"us-east", "eu-private"}, ExpiresAt: date("2026-11-15"), Status: "active"},
	{ID: "k4", Name: "web-chat", Prefix: "ngw_live_9a0c", Team: "web", Project: "assistant", AllowedModels: []string{"gpt-5-mini", "claude-haiku-4-5"}, AllowedRegions: []string{"us-east"}, ExpiresAt: date("2027-01-20"), Status: "rotating"},
	{ID: "k5", Name: "research", Prefix: "ngw_live_e55f", Team: "research", Project: "evals", AllowedModels: []string{"claude-opus-4-1", "gpt-5.5", "claude-sonnet-5"}, AllowedRegions: []string{"us-east"}, Status: "active"},
	{ID: "k6", Name: "secops-triage", Prefix: "ngw_live_41d2", Team: "security", Project: "soc", AllowedModels: []string{"llama-3.3-70b", "claude-haiku-4-5"}, AllowedRegions: []string{"eu-private", "eu-central"}, ExpiresAt: date("2026-10-02"), Status: "active"},
	{ID: "k7", Name: "legacy-intranet", Prefix: "ngw_live_77aa", Team: "web", Project: "intranet", AllowedModels: []string{"gpt-5-mini"}, AllowedRegions: []string{"us-east"}, ExpiresAt: date("2026-08-30"), Status: "revoked"},
}

// KeyWeights is the share of generated traffic per key (mock.ts keyWeights).
var KeyWeights = map[string]int{"k1": 30, "k2": 22, "k3": 10, "k4": 30, "k5": 5, "k6": 3}

var Budgets = []model.Budget{
	{ID: "b1", Scope: "support", ScopeType: "team", Period: "monthly", CapUSD: 12_000, OnExceed: "throttle"},
	{ID: "b2", Scope: "agents", ScopeType: "team", Period: "monthly", CapUSD: 40_000, OnExceed: "block"},
	{ID: "b3", Scope: "batch-summarize", ScopeType: "key", Period: "monthly", CapUSD: 8_000, OnExceed: "warn"},
	{ID: "b4", Scope: "web", ScopeType: "team", Period: "monthly", CapUSD: 15_000, OnExceed: "block"},
	{ID: "b5", Scope: "research", ScopeType: "team", Period: "monthly", CapUSD: 20_000, OnExceed: "warn"},
}

var Rules = []model.PolicyRule{
	{ID: "r1", Ordinal: 1, Name: "no-pii-out", Description: "Redact customer identifiers before they leave the perimeter.", Mode: "enforce", FailMode: "closed", Version: 7,
		When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email", "SSN"}}, {Field: "team", Op: "is not", Value: []string{"security"}}},
		Then: []model.Action{{Action: "redact", Detail: "email, SSN · rehydrate on return"}}},
	{ID: "r2", Ordinal: 2, Name: "eu-only", Description: "EU customer traffic must stay in EU regions.", Mode: "enforce", FailMode: "closed", Version: 3,
		When: []model.Cond{{Field: "header x-data-region", Op: "equals", Value: []string{"eu"}}},
		Then: []model.Action{{Action: "route to", Detail: "eu-private"}}},
	{ID: "r3", Ordinal: 3, Name: "block-src", Description: "Block proprietary source code and secrets from third-party providers.", Mode: "enforce", FailMode: "closed", Version: 12,
		When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"secret", "private key", "source code"}}, {Field: "provider", Op: "is not", Value: []string{"Self-hosted"}}},
		Then: []model.Action{{Action: "block", Detail: "return 403 with rule id"}}},
	{ID: "r4", Ordinal: 4, Name: "card-numbers", Description: "Luhn-validated card numbers are redacted everywhere.", Mode: "monitor", FailMode: "closed", Version: 1,
		When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"credit card"}}},
		Then: []model.Action{{Action: "redact", Detail: "credit card · no rehydrate"}}},
	{ID: "r5", Ordinal: 5, Name: "cost-guard-opus", Description: "Downgrade long-context batch jobs off Opus.", Mode: "enforce", FailMode: "open", Version: 2,
		When: []model.Cond{{Field: "model", Op: "equals", Value: []string{"claude-opus-4-1"}}, {Field: "team", Op: "is", Value: []string{"batch"}}},
		Then: []model.Action{{Action: "route to", Detail: "gpt-5-mini"}}},
}

var Detectors = []model.Detector{
	{ID: "email", Name: "Email address", Kind: "Built-in · regex", Threshold: 0.99, Hits24h: 842, FP: 3},
	{ID: "ssn", Name: "US SSN", Kind: "Built-in · regex + checksum", Threshold: 0.95, Hits24h: 61},
	{ID: "person", Name: "Person name", Kind: "Built-in · NER", Threshold: 0.82, Hits24h: 2_310, FP: 41},
	{ID: "card", Name: "Credit card", Kind: "Built-in · Luhn", Threshold: 1.0, Hits24h: 22, FP: 1},
	{ID: "secret", Name: "API secret", Kind: "Built-in · entropy", Threshold: 0.9, Hits24h: 88, FP: 12},
	{ID: "src", Name: "Source code", Kind: "Built-in · classifier", Threshold: 0.75, Hits24h: 31, FP: 6},
	{ID: "acct", Name: "Acme account ID", Kind: `Custom · regex  ACME-\d{8}`, Threshold: 1.0, Hits24h: 407},
}

// Changes are seeded audit rows, placed relative to seed time.
func Changes(now time.Time) []model.Change {
	ago := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	return []model.Change{
		{TS: ago(42 * time.Minute), Actor: "priya@acme.dev", Action: "Switched route target", Target: "default → claude-sonnet-5", TargetKind: "Route", Effect: "p50 latency −340ms · cost/request −38%", EffectTone: "good", Source: "console"},
		{TS: ago(216 * time.Minute), Actor: "argocd", Action: "Synced from Git", Target: "vllm-internal", TargetKind: "Backend", Effect: "Drift: replicas 4 → 2 · p95 +610ms on eu-private", EffectTone: "bad", Source: "git"},
		{TS: ago(306 * time.Minute), Actor: "marco@acme.dev", Action: "Published rule", Target: "block-src v12", TargetKind: "Policy", Effect: "Blocks 7× baseline · 82 from support", EffectTone: "bad", Source: "console"},
		{TS: ago(564 * time.Minute), Actor: "dana@acme.dev", Action: "Raised budget cap", Target: "agents $32,000 → $40,000", TargetKind: "Budget", Effect: "No throttled requests since", EffectTone: "good", Source: "console"},
		{TS: ago(20 * time.Hour), Actor: "priya@acme.dev", Action: "Rotated key", Target: "web-chat", TargetKind: "Key", Effect: "61% of traffic on new secret", EffectTone: "neutral", Source: "console"},
		{TS: ago(26 * time.Hour), Actor: "marco@acme.dev", Action: "Published rule in monitor mode", Target: "card-numbers v1", TargetKind: "Policy", Effect: "Would have redacted 22 requests", EffectTone: "neutral", Source: "console"},
	}
}
