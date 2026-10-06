package pricing

import (
	"math"
	"reflect"
	"testing"
)

func f(v float64) *float64 { return &v }

func all(in, cached, write, out, reasoning float64) Rates {
	return Rates{f(in), f(cached), f(write), f(out), f(reasoning)}
}

func srcs(s Source) Sources { return Sources{s, s, s, s, s} }

func near(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-12 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCostSplitsInputIntoUncachedCachedAndCacheWrites(t *testing.T) {
	r := all(2, 0.2, 2.5, 10, 10)
	// Cache reads and writes are part of input_tokens, so 1000 in with 200
	// read and 100 written bills 700 at the input rate.
	got := Cost(r, Tokens{Input: 1000, Cached: 200, CacheWrite: 100, Output: 500, Reasoning: 50})
	near(t, got, (700*2+200*0.2+100*2.5+450*10+50*10)/1e6) // the 50 reasoning are inside the 500 output
}

func TestCostIsNilWhenANeededRateIsMissing(t *testing.T) {
	r := all(2, 0.2, 2.5, 10, 10)
	r[CacheWrite] = nil
	if got := Cost(r, Tokens{Input: 1000, CacheWrite: 10, Output: 5}); got != nil {
		t.Fatalf("a cache write with no cache-write rate can't be priced: %v", *got)
	}
	// A missing rate the receipt doesn't use doesn't matter.
	near(t, Cost(r, Tokens{Input: 1000, Output: 5}), (1000*2+5*10)/1e6)
	if got := Cost(Rates{}, Tokens{}); got != nil {
		t.Fatalf("no price at all is no cost, not $0: %v", *got)
	}
}

func TestCostNeverBillsNegativeUncachedInput(t *testing.T) {
	near(t, Cost(all(1, 0.5, 1, 1, 1), Tokens{Input: 100, Cached: 80, CacheWrite: 40}), (80*0.5+40*1)/1e6)
}

const litellmFile = `{
  "sample_spec": {"input_cost_per_token": "price per token", "litellm_provider": "x"},
  "claude-sonnet-5": {"input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05,
    "cache_read_input_token_cost": 2e-07, "cache_creation_input_token_cost": 2.5e-06},
  "gpt-5-mini": {"input_cost_per_token": 2.5e-07, "output_cost_per_token": 2e-06, "cache_read_input_token_cost": 2.5e-08},
  "o-reasoner": {"input_cost_per_token": 1e-06, "output_cost_per_token": 4e-06, "output_cost_per_reasoning_token": 3e-06},
  "databricks/claude-opus-4-1": {"input_cost_per_token": 1.500002e-05, "output_cost_per_token": 7.500003e-05},
  "dall-e-3": {"output_cost_per_image": 0.04},
  "embed": {"input_cost_per_token": 1e-07}
}`

func TestParseLiteLLM(t *testing.T) {
	got, err := ParseLiteLLM([]byte(litellmFile))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Rates{
		"claude-sonnet-5": all(2, 0.2, 2.5, 10, 10),
		// No cache-write rate: cache writes bill as input. No reasoning rate:
		// reasoning bills as output.
		"gpt-5-mini": all(0.25, 0.025, 0.25, 2, 2),
		// No cache-read rate either: cached input bills as input.
		"o-reasoner": all(1, 1, 1, 4, 3),
		// Rates are kept to 6 decimal places per 1M, like model_pricing.
		"databricks/claude-opus-4-1": all(15.00002, 15.00002, 15.00002, 75.00003, 75.00003),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if _, err := ParseLiteLLM([]byte(`[1,2]`)); err == nil {
		t.Fatal("a file that isn't LiteLLM's object should fail, not sync nothing")
	}
}

func TestSyncAppliesLiteLLMToRatesNobodyOverrode(t *testing.T) {
	lite := all(2, 0.2, 2.5, 10, 10)
	p := PlanSync(Current{Rates: all(3, 0.3, 3, 15, 15), Sources: srcs(Seed)}, &lite, Seen{})
	if !p.Changed || !reflect.DeepEqual(p.Rates, lite) || p.Sources != srcs(LiteLLM) || len(p.Proposals) != 0 {
		t.Fatalf("seed rates move to LiteLLM's: %+v", p)
	}
	if !reflect.DeepEqual(p.Seen, Seen(lite)) {
		t.Fatalf("every LiteLLM value is remembered: %v", p.Seen)
	}
	again := PlanSync(Current{Rates: lite, Sources: srcs(LiteLLM)}, &lite, p.Seen)
	if again.Changed || len(again.Proposals) != 0 {
		t.Fatalf("nothing moved, nothing to write: %+v", again)
	}
}

func TestSyncProposesInsteadOfOverwritingAManualRate(t *testing.T) {
	cur := Current{Rates: all(1.8, 0.2, 2.5, 10, 10), Sources: srcs(LiteLLM)}
	cur.Sources[Input] = Manual
	seen := Seen(all(2, 0.2, 2.5, 10, 10))
	lite := all(2.4, 0.24, 2.5, 12, 12)
	p := PlanSync(cur, &lite, seen)
	want := all(1.8, 0.24, 2.5, 12, 12) // the override stays; the rest follow
	if !p.Changed || !reflect.DeepEqual(p.Rates, want) || p.Sources != cur.Sources {
		t.Fatalf("rates %v sources %v", p.Rates, p.Sources)
	}
	if !reflect.DeepEqual(p.Proposals, []Proposal{{Rate: Input, Current: 1.8, Proposed: 2.4}}) {
		t.Fatalf("proposals %+v", p.Proposals)
	}
	if *p.Seen[Input] != 2.4 {
		t.Fatalf("LiteLLM's new input rate is remembered, so a dismissal holds until it moves again: %v", *p.Seen[Input])
	}
}

func TestSyncDoesNotProposeWhenLiteLLMHasNotMovedSinceTheOverride(t *testing.T) {
	cur := Current{Rates: all(1.8, 0.2, 2.5, 10, 10), Sources: srcs(LiteLLM)}
	cur.Sources[Input] = Manual
	lite := all(2, 0.2, 2.5, 10, 10)
	if p := PlanSync(cur, &lite, Seen(lite)); p.Changed || len(p.Proposals) != 0 {
		t.Fatalf("unchanged LiteLLM, kept override: %+v", p)
	}
	// Never seen before (the override predates the first sync): remember, don't propose.
	if p := PlanSync(cur, &lite, Seen{}); len(p.Proposals) != 0 || p.Seen[Input] == nil {
		t.Fatalf("first sight: %+v", p)
	}
	// LiteLLM moved to exactly the override: nothing to accept.
	moved := all(1.8, 0.2, 2.5, 10, 10)
	if p := PlanSync(cur, &moved, Seen(lite)); len(p.Proposals) != 0 {
		t.Fatalf("moved onto the override: %+v", p.Proposals)
	}
}

func TestSyncRetiresSeedRatesLiteLLMDoesNotCover(t *testing.T) {
	p := PlanSync(Current{Rates: all(0.12, 0.12, 0.12, 0.3, 0.3), Sources: srcs(Seed)}, nil, Seen{})
	if !p.Changed || p.Rates != (Rates{}) || p.Sources != (Sources{}) {
		t.Fatalf("an unmapped seed pair has no price after the first sync: %+v", p)
	}
	cur := Current{Rates: all(0.12, 0.12, 0.12, 0.3, 0.3), Sources: srcs(Seed)}
	cur.Sources[Output] = Manual
	p = PlanSync(cur, nil, Seen{})
	if p.Rates[Output] == nil || *p.Rates[Output] != 0.3 || p.Rates[Input] != nil || p.Sources[Output] != Manual {
		t.Fatalf("a manual rate survives retirement: %+v", p)
	}
}

func TestSyncKeepsTheLastLiteLLMRateWhenTheEntryDisappears(t *testing.T) {
	cur := Current{Rates: all(2, 0.2, 2.5, 10, 10), Sources: srcs(LiteLLM)}
	if p := PlanSync(cur, nil, Seen(cur.Rates)); p.Changed {
		t.Fatalf("a key that vanished from the file keeps its last rate: %+v", p)
	}
}

func TestSyncPricesAPairThatHadNoPrice(t *testing.T) {
	lite := all(1.1, 0.11, 1.375, 5.5, 5.5)
	p := PlanSync(Current{}, &lite, Seen{})
	if !p.Changed || !reflect.DeepEqual(p.Rates, lite) || p.Sources != srcs(LiteLLM) {
		t.Fatalf("%+v", p)
	}
}

func TestSyncComparesAtStoredPrecision(t *testing.T) {
	// 0.25/10 isn't exactly 0.025 in floating point; the database returns 0.025.
	lite := Rates{f(0.25), f(0.25 / 10), f(0.25 * 1.25), f(2), f(2)}
	stored := all(0.25, 0.025, 0.3125, 2, 2)
	if p := PlanSync(Current{Rates: stored, Sources: srcs(LiteLLM)}, &lite, Seen(stored)); p.Changed {
		t.Fatalf("a rate equal to 6 decimal places isn't a change: %+v", p)
	}
}

func TestPriced(t *testing.T) {
	r := all(1, 1, 1, 1, 1)
	if !r.Priced() {
		t.Fatal("all five rates set")
	}
	r[Reasoning] = nil
	if r.Priced() || (Rates{}).Priced() {
		t.Fatal("any missing rate means the pair isn't fully priced")
	}
}

// Beside prices, the file says what a model takes in and when it's retired.
func TestParseFacts(t *testing.T) {
	file := []byte(`{
		"sample_spec": {"mode": "chat"},
		"gpt-5-mini": {"mode": "chat", "supports_vision": true, "supports_pdf_input": true, "input_cost_per_token": 1e-7, "output_cost_per_token": 1e-6},
		"eu.anthropic.claude-haiku-4-5-20251001-v1:0": {"mode": "chat", "supports_vision": true, "deprecation_date": "2026-10-15"},
		"whisper-1": {"mode": "audio_transcription", "supports_audio_input": true},
		"text-embedding-3-small": {"mode": "embedding", "deprecation_date": "not a date"}
	}`)
	got, err := ParseFacts(file)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Facts{
		"gpt-5-mini": {Modalities: []string{"text", "image", "pdf"}},
		"eu.anthropic.claude-haiku-4-5-20251001-v1:0": {Modalities: []string{"text", "image"}, Deprecation: "2026-10-15"},
		"whisper-1":              {Modalities: []string{"audio"}},
		"text-embedding-3-small": {Modalities: []string{"text"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// Output counts include reasoning, as Agent Router logs OpenAI's
// completion_tokens (decisions §1): reasoning bills once, at its own rate,
// and the rest of the output at the output rate.
func TestCostBillsReasoningOnceInsideOutput(t *testing.T) {
	r := all(1, 0.1, 1.25, 10, 20) // per 1M tokens
	near(t, Cost(r, Tokens{Output: 1000, Reasoning: 400}), (600*10+400*20)/1e6)
	// More reasoning than output (a provider that reports them apart) never bills negative output.
	near(t, Cost(r, Tokens{Output: 100, Reasoning: 400}), 400*20/1e6)
}
