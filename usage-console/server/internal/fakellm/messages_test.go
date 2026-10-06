package fakellm

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// Like Anthropic's OpenAI-compatible endpoint, the Anthropic backend's chat
// completions take its key as a bearer token, and nothing else.
func TestAnthropicBackendsOpenAIEndpointTakesItsKey(t *testing.T) {
	for auth, want := range map[string]bool{"Bearer " + AnthropicKey: true, "Bearer " + KeyedKey: false, "": false, AnthropicKey: false} {
		if got := Authorized(AnthropicBackend, auth); got != want {
			t.Errorf("Authorized(keyed-anthropic, %q) = %v, want %v", auth, got, want)
		}
	}
}

func messagesRequest(t *testing.T, s string) AnthropicRequest {
	t.Helper()
	var r AnthropicRequest
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// AnthropicChat reads a Messages request as the simulator's chat request:
// the system prompt and each message's text.
func TestAnthropicChat(t *testing.T) {
	cr := AnthropicChat(messagesRequest(t, `{"model":"claude-sonnet-5","max_tokens":300,"stream":true,"system":[{"type":"text","text":"be brief"}],
		"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"text","text":"hi "},{"type":"image","source":{}},{"type":"text","text":"there"}]}]}`))
	var got []string
	for _, m := range cr.Messages {
		got = append(got, m.Role+":"+m.Content)
	}
	if cr.Model != "claude-sonnet-5" || cr.MaxTokens != 300 || !cr.Stream || strings.Join(got, "|") != "system:be brief|user:first|assistant:ok|user:hi there" {
		t.Errorf("chat = %+v", cr)
	}
}

// A Messages reply reports usage Anthropic's way: input_tokens excludes the
// cache reads and writes, which have their own fields.
func TestAnthropicMessageUsage(t *testing.T) {
	p := Plan{Status: 200, Chunks: []string{"Hello", " there"}}
	p.Usage.PromptTokens, p.Usage.CompletionTokens = 100, 7
	p.Usage.PromptTokensDetails.CachedTokens, p.Usage.PromptTokensDetails.CacheCreationTokens = 30, 20
	b, _ := json.Marshal(AnthropicMessage("msg_1", "claude-sonnet-5", p))
	var m struct {
		ID, Type, Role, Model string
		StopReason            string `json:"stop_reason"`
		Content               []struct{ Type, Text string }
		Usage                 map[string]int
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "msg_1" || m.Type != "message" || m.Role != "assistant" || m.StopReason != "end_turn" || len(m.Content) != 1 || m.Content[0].Text != "Hello there" {
		t.Errorf("message = %s", b)
	}
	if want := map[string]int{"input_tokens": 50, "cache_read_input_tokens": 30, "cache_creation_input_tokens": 20, "output_tokens": 7}; !mapsEqual(m.Usage, want) {
		t.Errorf("usage = %v, want %v", m.Usage, want)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// A streamed reply is Anthropic's event sequence, usage split as Anthropic
// sends it: input on message_start, output on message_delta.
func TestAnthropicEvents(t *testing.T) {
	p := Plan{Status: 200, Chunks: []string{"Hel", "lo"}}
	p.Usage.PromptTokens, p.Usage.CompletionTokens = 12, 2
	evs := AnthropicEvents("msg_1", "claude-echo", p)
	var names []string
	var text string
	for _, e := range evs {
		names = append(names, e.Name)
		b, _ := json.Marshal(e.Data)
		var d struct {
			Type    string
			Delta   struct{ Text string }
			Message struct{ Usage map[string]int }
			Usage   map[string]int
		}
		_ = json.Unmarshal(b, &d)
		if d.Type != e.Name {
			t.Errorf("event %s carries type %s", e.Name, d.Type)
		}
		text += d.Delta.Text
		switch e.Name {
		case "message_start":
			if d.Message.Usage["input_tokens"] != 12 {
				t.Errorf("message_start usage = %v", d.Message.Usage)
			}
		case "message_delta":
			if d.Usage["output_tokens"] != 2 {
				t.Errorf("message_delta usage = %v", d.Usage)
			}
		}
	}
	if got := strings.Join(names, " "); got != "message_start content_block_start content_block_delta content_block_delta content_block_stop message_delta message_stop" || text != "Hello" {
		t.Errorf("events = %s, text %q", got, text)
	}
}

// Failures come back in Anthropic's shape: 529 is overloaded.
func TestAnthropicFailure(t *testing.T) {
	var e struct {
		Type  string
		Error struct{ Type, Message string }
	}
	if err := json.Unmarshal([]byte(AnthropicFailure(Plan{Status: 529, Error: "overloaded"})), &e); err != nil || e.Type != "error" || e.Error.Type != "overloaded_error" {
		t.Errorf("529 = %+v", e)
	}
	if err := json.Unmarshal([]byte(AnthropicFailure(Plan{Status: 429, Error: "rate limited"})), &e); err != nil || e.Error.Type != "rate_limit_error" {
		t.Errorf("429 = %+v", e)
	}
}

// The simulated backends answer Messages too (anthropic-prod is Anthropic,
// reached natively by Anthropic-style callers), with their usual profile.
func TestSimulateMessages(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	cr := AnthropicChat(messagesRequest(t, `{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
	ok := 0
	for range 50 {
		p := Simulate("openai-prod", cr, r)
		if p.Status == 200 {
			ok++
			if p.Usage.PromptTokens == 0 || p.Usage.CompletionTokens == 0 || p.Duration <= 0 || p.Duration > 10*time.Second {
				t.Fatalf("plan = %+v", p)
			}
		}
	}
	if ok < 40 {
		t.Errorf("%d of 50 succeeded", ok)
	}
}
