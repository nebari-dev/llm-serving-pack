package fakellm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The keyed backend takes a second key too, so replacing one good key with
// another can be told apart through the gateway (KeyedKeyNumber).
func TestKeyedBackendTakesTwoKeys(t *testing.T) {
	for auth, want := range map[string]int{
		"Bearer " + KeyedKey:  1,
		"Bearer " + KeyedKey2: 2,
		"Bearer sk-wrong":     0,
		KeyedKey2:             0,
	} {
		if got := KeyedKeyNumber(auth); got != want {
			t.Errorf("KeyedKeyNumber(%q) = %d, want %d", auth, got, want)
		}
	}
	if !Authorized(KeyedBackend, "Bearer "+KeyedKey2) {
		t.Errorf("the second key isn't authorized")
	}
}

// The Anthropic backend speaks Anthropic's native API: x-api-key and
// anthropic-version, never a bearer token, with Anthropic's error shapes.
func TestAnthropicAuth(t *testing.T) {
	for _, tc := range []struct {
		key, version string
		status       int
		says         string
	}{
		{AnthropicKey, "2023-06-01", 0, ""},
		{"", "2023-06-01", 401, "authentication_error"},
		{"sk-ant-wrong-0000000000", "2023-06-01", 401, "invalid x-api-key"},
		{AnthropicKey, "", 400, "anthropic-version"},
	} {
		status, body := AnthropicAuth(tc.key, tc.version)
		if status != tc.status || !strings.Contains(body, tc.says) {
			t.Errorf("AnthropicAuth(%q, %q) = %d %s", tc.key, tc.version, status, body)
		}
		if status != 0 {
			var e struct {
				Type  string `json:"type"`
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &e); err != nil || e.Type != "error" || e.Error.Type == "" {
				t.Errorf("not an Anthropic error: %s", body)
			}
		}
	}
	if !IsAnthropic(AnthropicBackend) || IsAnthropic(KeyedBackend) {
		t.Errorf("IsAnthropic")
	}
}

func TestAnthropicModelsAndEcho(t *testing.T) {
	b, _ := json.Marshal(AnthropicModels())
	var list struct {
		Data    []struct{ ID, Type string } `json:"data"`
		HasMore bool                        `json:"has_more"`
	}
	if err := json.Unmarshal(b, &list); err != nil || len(list.Data) == 0 || list.Data[0].ID != AnthropicModel || list.Data[0].Type != "model" {
		t.Errorf("models = %s", b)
	}

	var req AnthropicRequest
	_ = json.Unmarshal([]byte(`{"model":"claude-echo","max_tokens":16,"system":"be brief","messages":[
		{"role":"user","content":"first"},{"role":"assistant","content":"ok"},
		{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"anthropic"}]}]}`), &req)
	res := AnthropicReply(req)
	b, _ = json.Marshal(res)
	var got struct {
		Type, Role, Model string
		StopReason        string `json:"stop_reason"`
		Content           []struct{ Type, Text string }
		Usage             struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "message" || got.Role != "assistant" || got.Model != "claude-echo" || got.StopReason != "end_turn" ||
		len(got.Content) != 1 || got.Content[0].Text != "You said: hello anthropic" || got.Usage.InputTokens == 0 || got.Usage.OutputTokens == 0 {
		t.Errorf("reply = %s", b)
	}
}
