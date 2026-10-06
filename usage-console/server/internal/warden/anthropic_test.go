package warden

import (
	"encoding/json"
	"strings"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/jbouder/stargate/server/internal/gateway"
)

const messagesPath = "/anthropic/v1/messages"

// anthropicHeaders are what reaches Warden for an Anthropic-style request
// the key check admitted for keyID.
func anthropicHeaders(snap *gateway.Snapshot, keyID string, extra ...string) map[string]string {
	return headers(snap, keyID, append([]string{":path", messagesPath, "x-stargate-api", "anthropic"}, extra...)...)
}

// Anthropic's Messages API keeps text in more places than OpenAI's: a
// system prompt (string or text blocks), string content, text blocks, and
// tool results. Warden inspects and redacts each, and leaves everything
// else as sent: images, tool calls, tools, cache_control, numbers.
func TestAnthropicRequestRedactsEveryText(t *testing.T) {
	snap := gateway.DemoSnapshot()
	in := []byte(`{"model":"gpt-5-mini","max_tokens":1024,"temperature":0.2,"metadata":{"user_id":"u-1"},` +
		`"system":[{"type":"text","text":"Reply to jo@example.com","cache_control":{"type":"ephemeral"}}],` +
		`"tools":[{"name":"lookup","description":"find a@b.com","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"user","content":"mail jordan@example.com please"},` +
		`{"role":"assistant","content":[{"type":"text","text":"Looking up."},{"type":"tool_use","id":"tu_1","name":"lookup","input":{"q":"x@y.com"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"owner is sam@example.org"}]},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}},{"type":"text","text":"and cc kim@example.net"}]}` +
		`]}`)
	r := newServer(snap).decide(anthropicHeaders(snap, "k1"), in)
	p := policyOf(t, r)
	if p.Verdict != "redacted" || len(p.Redactions) != 1 || p.Redactions[0].Type != "email" || p.Redactions[0].Count != 4 {
		t.Fatalf("policy = %+v", p)
	}
	raw := r.GetRequestBody().GetResponse().GetBodyMutation().GetBody()
	if raw == nil {
		t.Fatal("body not rewritten")
	}
	got := string(raw)
	for _, s := range []string{"jo@example.com", "jordan@example.com", "sam@example.org", "kim@example.net"} {
		if strings.Contains(got, s) {
			t.Errorf("%s reached the provider: %s", s, got)
		}
	}
	var b struct {
		System []struct {
			Text         string         `json:"text"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens   json.Number    `json:"max_tokens"`
		Temperature json.Number    `json:"temperature"`
		Metadata    map[string]any `json:"metadata"`
		Model       string         `json:"model"`
	}
	d := json.NewDecoder(strings.NewReader(got))
	d.UseNumber()
	if err := d.Decode(&b); err != nil {
		t.Fatal(err)
	}
	if b.System[0].Text != "Reply to [EMAIL_1]" || b.System[0].CacheControl["type"] != "ephemeral" {
		t.Errorf("system = %+v", b.System)
	}
	if string(b.Messages[0].Content) != `"mail [EMAIL_2] please"` {
		t.Errorf("string content = %s", b.Messages[0].Content)
	}
	// What Warden doesn't inspect goes as sent: the model's own tool call
	// (OpenAI's tool calls aren't inspected either), the tools, the image.
	for _, s := range []string{`"input":{"q":"x@y.com"}`, `"description":"find a@b.com"`, `"data":"iVBOR"`, `"text":"Looking up."`} {
		if !strings.Contains(got, s) {
			t.Errorf("rewritten body lacks %s: %s", s, got)
		}
	}
	if !strings.Contains(string(b.Messages[2].Content), `"text":"owner is [EMAIL_3]"`) || !strings.Contains(string(b.Messages[2].Content), `"text":"and cc [EMAIL_4]"`) {
		t.Errorf("tool result and text block = %s", b.Messages[2].Content)
	}
	if b.MaxTokens != "1024" || b.Temperature != "0.2" || b.Metadata["user_id"] != "u-1" || b.Model != "gpt-5-mini" {
		t.Errorf("fields Warden doesn't own changed: %s", got)
	}
}

// A system prompt may be a plain string.
func TestAnthropicStringSystemPromptIsRedacted(t *testing.T) {
	snap := gateway.DemoSnapshot()
	in := []byte(`{"model":"gpt-5-mini","max_tokens":64,"system":"cc jo@example.com","messages":[{"role":"user","content":"hello"}]}`)
	r := newServer(snap).decide(anthropicHeaders(snap, "k1"), in)
	var b map[string]any
	if err := json.Unmarshal(r.GetRequestBody().GetResponse().GetBodyMutation().GetBody(), &b); err != nil || b["system"] != "cc [EMAIL_1]" {
		t.Fatalf("body = %v (%v)", b, err)
	}
}

// A block answers in Anthropic's error shape, so the Anthropic SDK raises
// its PermissionDeniedError with the rule's message.
func TestAnthropicBlockAnswersInAnthropicsShape(t *testing.T) {
	snap := gateway.DemoSnapshot()
	in := []byte(`{"model":"gpt-5-mini","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"OPENAI_KEY=sk-` + strings.Repeat("a", 24) + `"}]}]}`)
	ir := newServer(snap).decide(anthropicHeaders(snap, "k1"), in).GetImmediateResponse()
	var e struct {
		Type  string
		Error struct{ Type, Code, Message string }
	}
	if err := json.Unmarshal(ir.GetBody(), &e); err != nil || ir.GetStatus().GetCode() != 403 || e.Type != "error" || e.Error.Type != "permission_error" ||
		e.Error.Code != "policy_blocked" || !strings.Contains(e.Error.Message, "block-src") {
		t.Fatalf("immediate = %d %s", ir.GetStatus().GetCode(), ir.GetBody())
	}
}

// A reroute rewrites the Messages request's model and hints the backend, as
// for OpenAI-style callers.
func TestAnthropicRerouteRewritesModel(t *testing.T) {
	snap := gateway.DemoSnapshot()
	r := newServer(snap).decide(anthropicHeaders(snap, "k3", "x-data-region", "eu"), []byte(`{"model":"summarize-digest","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	b, hs := mutated(r)
	if b["model"] != "llama-3.3-70b" || hs[HeaderBackend] != "vllm-internal" || b["max_tokens"] != 64.0 {
		t.Errorf("body %v, hint %q", b, hs[HeaderBackend])
	}
}

// Rehydration reads Anthropic's response shapes: a message's content blocks…
func TestAnthropicJSONResponseRehydrates(t *testing.T) {
	v := vault(t)
	r := newBodyRehydrator(v, "application/json", gateway.APIAnthropic)
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-echo","content":[{"type":"text","text":"Sent to [EMAIL_1]"},` +
		`{"type":"tool_use","id":"tu_1","name":"send","input":{"to":"[EMAIL_2]"}}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":9007199254740993}}`
	out := feedAll(r, body[:50], body[50:])
	if out[0] != "" {
		t.Fatalf("emitted before the end: %q", out[0])
	}
	for _, s := range []string{`"text":"Sent to a@b.com"`, `"to":"c@d.org"`, `"output_tokens":9007199254740993`, `"id":"msg_1"`} {
		if !strings.Contains(out[1], s) {
			t.Errorf("caller gets %s, want %s", out[1], s)
		}
	}
}

func anthropicEvent(name string, v any) string {
	b, _ := json.Marshal(v)
	return "event: " + name + "\ndata: " + string(b) + "\n\n"
}

func textDelta(i int, field, kind, s string) string {
	return anthropicEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": kind, field: s}})
}

func blockStop(i int) string {
	return anthropicEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
}

// deltas is the caller's view of a Messages stream: each content block's
// text, partial_json or thinking, joined.
func deltas(t *testing.T, stream string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, ev := range strings.Split(stream, "\n\n") {
		for _, l := range strings.Split(ev, "\n") {
			data, ok := strings.CutPrefix(l, "data: ")
			if !ok {
				continue
			}
			var e struct {
				Type  string
				Index int
				Delta map[string]any
			}
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("event %q: %v", data, err)
			}
			if e.Type != "content_block_delta" {
				continue
			}
			for _, f := range []string{"text", "partial_json", "thinking"} {
				if s, ok := e.Delta[f].(string); ok {
					out[f+string(rune('0'+e.Index))] += s
				}
			}
		}
	}
	return out
}

// …and its event stream, where a placeholder can arrive over several deltas
// of a content block: what might be one is held until it's clear, and what's
// still held goes out before the block stops.
func TestAnthropicStreamRehydrates(t *testing.T) {
	v := vault(t)
	r := newBodyRehydrator(v, "text/event-stream", gateway.APIAnthropic)
	start := anthropicEvent("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1", "usage": map[string]any{"input_tokens": 9, "output_tokens": 1}}}) +
		anthropicEvent("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	chunks := []string{
		start + textDelta(0, "text", "text_delta", "Hi [EM"),
		textDelta(0, "text", "text_delta", "AIL_1], and [EMA"),
		blockStop(0) + anthropicEvent("content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "tu_1", "name": "send", "input": map[string]any{}}}),
		textDelta(1, "partial_json", "input_json_delta", `{"to":"[EMAIL`) + textDelta(1, "partial_json", "input_json_delta", `_2]"}`),
		blockStop(1) + anthropicEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 20}}) +
			anthropicEvent("message_stop", map[string]any{"type": "message_stop"}),
	}
	out := strings.Join(feedAll(r, chunks...), "")
	got := deltas(t, out)
	if got["text0"] != "Hi a@b.com, and [EMA" || got["partial_json1"] != `{"to":"c@d.org"}` {
		t.Fatalf("caller gets %q from\n%s", got, out)
	}
	// Held text goes out as a delta of its own block, before the block stops.
	if i, j := strings.Index(out, `"text":"[EMA"`), strings.Index(out, `{"index":0,"type":"content_block_stop"}`); i < 0 || j < 0 || i > j {
		t.Errorf("held text not flushed before its block stops:\n%s", out)
	}
	for _, s := range []string{"event: message_start", `"output_tokens":20`, "event: message_stop", `"type":"text_delta"`, `"type":"input_json_delta"`} {
		if !strings.Contains(out, s) {
			t.Errorf("stream lacks %s:\n%s", s, out)
		}
	}
}

// End to end through the ext_proc stream: an Anthropic-style request is
// redacted on the way in and its response restored on the way out.
func TestProcessAnthropicRedactsAndRehydrates(t *testing.T) {
	snap := gateway.DemoSnapshot()
	k := snap.KeyByID("k1")
	resp := `{"type":"message","content":[{"type":"text","text":"Hello [EMAIL_1]"}],"usage":{"input_tokens":5,"output_tokens":3}}`
	f := &fakeStream{in: []*extprocv3.ProcessingRequest{
		headerMsg(false, ":path", messagesPath, "x-stargate-key-id", k.ID, "x-stargate-team", k.Team, "x-stargate-project", k.Project, "x-request-id", "req-1"),
		bodyMsg(false, `{"model":"gpt-5-mini","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"mail a@b.com"}]}]}`, true),
		headerMsg(true, "content-type", "application/json"),
		bodyMsg(true, resp, true),
	}}
	if err := newServer(snap).Process(f); err != nil {
		t.Fatal(err)
	}
	if got := string(f.out[1].GetRequestBody().GetResponse().GetBodyMutation().GetBody()); !strings.Contains(got, `"text":"mail [EMAIL_1]"`) {
		t.Fatalf("request = %s", got)
	}
	if got := chunkOut(t, f.out[3], resp); !strings.Contains(got, `"text":"Hello a@b.com"`) {
		t.Fatalf("caller gets %s", got)
	}
	if p := policyOf(t, f.out[3]); p.Redactions[0].Rehydrated != 1 {
		t.Fatalf("policy = %+v", p)
	}
}

// A Messages request Warden can't read (no model) fails to the rules' fail
// mode as an OpenAI one does, answering in Anthropic's shape.
func TestAnthropicUnreadableBodyFailsInAnthropicsShape(t *testing.T) {
	snap := gateway.DemoSnapshot()
	ir := newServer(snap).decide(anthropicHeaders(snap, "k1"), []byte(`{"messages":"nope"}`)).GetImmediateResponse()
	if ir.GetStatus().GetCode() != 503 || !strings.Contains(string(ir.GetBody()), `"type":"error"`) || !strings.Contains(string(ir.GetBody()), `"api_error"`) {
		t.Fatalf("immediate = %d %s", ir.GetStatus().GetCode(), ir.GetBody())
	}
}
