package fakellm

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// AnthropicBackend stands in for Anthropic's native API (not its
// OpenAI-compatible endpoint): GET /{backend}/v1/models and POST
// /{backend}/v1/messages, authenticated by x-api-key with an
// anthropic-version header, never a bearer token. It answers only
// AnthropicKey, and serves AnthropicModel by echoing the prompt.
const (
	AnthropicBackend = "keyed-anthropic"
	AnthropicKey     = "sk-ant-fake-3c9e1f0a6b2d4c87"
	AnthropicModel   = "claude-echo"
)

// IsAnthropic is whether a fake backend speaks Anthropic's API.
func IsAnthropic(backend string) bool { return backend == AnthropicBackend }

func anthropicError(kind, msg string) string {
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": msg}})
	return string(b)
}

// AnthropicAuth checks a request's x-api-key and anthropic-version headers
// as Anthropic does: 0 when they pass, else the status and error body.
func AnthropicAuth(apiKey, version string) (int, string) {
	switch {
	case apiKey == "":
		return 401, anthropicError("authentication_error", "x-api-key header is required")
	case apiKey != AnthropicKey:
		return 401, anthropicError("authentication_error", "invalid x-api-key")
	case version == "":
		return 400, anthropicError("invalid_request_error", "anthropic-version: header is required")
	}
	return 0, ""
}

// AnthropicModels is GET /v1/models in Anthropic's shape.
func AnthropicModels() any {
	var data []any
	for _, m := range ModelsFor(AnthropicBackend) {
		data = append(data, map[string]any{"type": "model", "id": m, "display_name": m, "created_at": "2026-01-01T00:00:00Z"})
	}
	return map[string]any{"data": data, "has_more": false, "first_id": AnthropicModel, "last_id": AnthropicModel}
}

// AnthropicRequest is the part of a Messages API request the fake reads. A
// message's content is a string or a list of blocks.
type AnthropicRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	System json.RawMessage `json:"system,omitempty"`
	Stream bool            `json:"stream,omitempty"`
}

// text is a content field's text: the string, or its text blocks joined.
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct{ Type, Text string }
	_ = json.Unmarshal(raw, &blocks)
	var b strings.Builder
	for _, x := range blocks {
		if x.Type == "text" {
			b.WriteString(x.Text)
		}
	}
	return b.String()
}

// AnthropicReply answers a Messages request by echoing its last user message,
// in Anthropic's response shape with its usage fields.
func AnthropicReply(req AnthropicRequest) any {
	last, in := "", EstimateTokens(text(req.System))
	for _, m := range req.Messages {
		t := text(m.Content)
		in += EstimateTokens(t) + 4
		if m.Role == "user" {
			last = t
		}
	}
	out := "You said: " + last
	return map[string]any{
		"id": "msg_fake" + strconv.FormatInt(time.Now().UnixNano(), 36), "type": "message", "role": "assistant", "model": req.Model,
		"content":     []any{map[string]string{"type": "text", "text": out}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": in, "output_tokens": EstimateTokens(out), "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
	}
}
