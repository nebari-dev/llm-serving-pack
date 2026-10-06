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
	return AnthropicMessage("msg_fake"+strconv.FormatInt(time.Now().UnixNano(), 36), req.Model, Echo(AnthropicChat(req)))
}

// AnthropicChat is a Messages request as the simulator reads it: the system
// prompt, then each message, as text.
func AnthropicChat(req AnthropicRequest) ChatRequest {
	cr := ChatRequest{Model: req.Model, MaxTokens: req.MaxTokens, Stream: req.Stream}
	if s := text(req.System); s != "" {
		cr.Messages = append(cr.Messages, Message{Role: "system", Content: s})
	}
	for _, m := range req.Messages {
		cr.Messages = append(cr.Messages, Message{Role: m.Role, Content: text(m.Content)})
	}
	return cr
}

// anthropicUsage is a plan's usage as Anthropic reports it: input_tokens
// leaves out the cache reads and writes, which have fields of their own.
func anthropicUsage(u Usage) map[string]int {
	cached, written := u.PromptTokensDetails.CachedTokens, u.PromptTokensDetails.CacheCreationTokens
	return map[string]int{"input_tokens": u.PromptTokens - cached - written, "cache_read_input_tokens": cached,
		"cache_creation_input_tokens": written, "output_tokens": u.CompletionTokens}
}

// AnthropicMessage is a successful plan as a Messages response.
func AnthropicMessage(id, model string, p Plan) any {
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content":     []any{map[string]string{"type": "text", "text": p.Content()}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": anthropicUsage(p.Usage),
	}
}

// AnthropicEvent is one server-sent event of a Messages stream.
type AnthropicEvent struct {
	Name string
	Data any
}

// AnthropicEvents is a successful plan as a Messages stream: input usage on
// message_start, a text block with a delta per chunk, output usage on
// message_delta.
func AnthropicEvents(id, model string, p Plan) []AnthropicEvent {
	start := anthropicUsage(p.Usage)
	start["output_tokens"] = 1 // as Anthropic sends it; the total comes on message_delta
	evs := []AnthropicEvent{
		{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": start}}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}}},
	}
	for _, c := range p.Chunks {
		evs = append(evs, AnthropicEvent{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": c}}})
	}
	return append(evs,
		AnthropicEvent{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		AnthropicEvent{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]int{"output_tokens": p.Usage.CompletionTokens}}},
		AnthropicEvent{"message_stop", map[string]any{"type": "message_stop"}},
	)
}

// AnthropicFailure is a failed plan's body, in Anthropic's error shape.
func AnthropicFailure(p Plan) string {
	kind := "api_error"
	switch p.Status {
	case 429:
		kind = "rate_limit_error"
	case 529:
		kind = "overloaded_error"
	}
	return anthropicError(kind, p.Error)
}
