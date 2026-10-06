package warden

import (
	"encoding/json"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	procmodev3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/gateway"
)

func gatewayInput(user string) gateway.Input {
	return gateway.Input{Req: fakellm.ChatRequest{Model: "gpt-5-mini", Messages: []fakellm.Message{{Role: "user", Content: user}}}, Now: time.Now()}
}

// vault is what a request to k1 mailing a@b.com leaves behind: seeded rule r1
// redacts email and says "rehydrate on return".
func vault(t *testing.T) *gateway.Vault {
	t.Helper()
	snap := gateway.DemoSnapshot()
	d := gateway.AdmitKey(snap, snap.KeyByID("k1"), gatewayInput("mail a@b.com or c@d.org"), rand.New(rand.NewPCG(1, 2)))
	if d.Vault.Len() != 2 {
		t.Fatalf("vault holds %d", d.Vault.Len())
	}
	return d.Vault
}

func feedAll(r *bodyRehydrator, chunks ...string) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = string(r.Feed([]byte(c), i == len(chunks)-1))
	}
	return out
}

func TestJSONBodyRehydratesContentAndToolArguments(t *testing.T) {
	v := vault(t)
	r := newBodyRehydrator(v, "application/json", gateway.APIOpenAI)
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1730000000,"model":"gpt-5-mini","choices":[{"index":0,"finish_reason":"tool_calls",` +
		`"message":{"role":"assistant","content":"Sent to [EMAIL_1] <ok>","tool_calls":[{"id":"t1","type":"function","function":{"name":"send","arguments":"{\"to\":\"[EMAIL_2]\"}"}}]}}],` +
		`"usage":{"prompt_tokens":12,"completion_tokens":9007199254740993}}`
	// Held whole until the end: a placeholder can straddle any two chunks.
	out := feedAll(r, body[:40], body[40:131], body[131:])
	if out[0] != "" || out[1] != "" {
		t.Fatalf("emitted before the end: %q", out[:2])
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct{ Arguments string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens json.Number `json:"completion_tokens"`
		} `json:"usage"`
	}
	d := json.NewDecoder(strings.NewReader(out[2]))
	d.UseNumber()
	if err := d.Decode(&got); err != nil {
		t.Fatalf("%v: %s", err, out[2])
	}
	m := got.Choices[0].Message
	if m.Content != "Sent to a@b.com <ok>" || m.ToolCalls[0].Function.Arguments != `{"to":"c@d.org"}` {
		t.Fatalf("message = %+v", m)
	}
	if got.Usage.CompletionTokens != "9007199254740993" || strings.Contains(out[2], "\\u003c") {
		t.Fatalf("rewrote more than strings (tokens %q): %s", got.Usage.CompletionTokens, out[2])
	}
	if v.Restored()["email"] != 2 {
		t.Fatalf("restored = %v", v.Restored())
	}
}

// A body with no placeholder in it goes back byte for byte.
func TestJSONBodyWithoutPlaceholdersIsUnchanged(t *testing.T) {
	body := `{"choices": [{"index": 0, "message": {"content": "no [list] here"}}]}`
	out := feedAll(newBodyRehydrator(vault(t), "application/json; charset=utf-8", gateway.APIOpenAI), body[:10], body[10:])
	if out[1] != body {
		t.Fatalf("got %q", out[1])
	}
}

func sse(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

func chunk(content string, finish ...string) string {
	c := map[string]any{"index": 0, "delta": map[string]any{}}
	if content != "" {
		c["delta"] = map[string]any{"content": content}
	}
	if len(finish) > 0 {
		c["finish_reason"] = finish[0]
	}
	return sse(map[string]any{"id": "c1", "object": "chat.completion.chunk", "model": "gpt-5-mini", "choices": []any{c}})
}

// contents reads the delta contents back out of an SSE stream, in order.
func contents(t *testing.T, stream string) []string {
	t.Helper()
	var out []string
	for _, ev := range strings.Split(strings.TrimSuffix(stream, "\n\n"), "\n\n") {
		data := strings.TrimPrefix(ev, "data: ")
		if data == "[DONE]" {
			out = append(out, "[DONE]")
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("event %q: %v", ev, err)
		}
		s := c.Choices[0].Delta.Content
		if c.Choices[0].FinishReason != "" {
			s += "<" + c.Choices[0].FinishReason + ">"
		}
		out = append(out, s)
	}
	return out
}

// The model streams a placeholder over two events: the first event goes out
// without the partial placeholder, the next carries the restored value.
func TestSSEPlaceholderSplitAcrossEvents(t *testing.T) {
	r := newBodyRehydrator(vault(t), "text/event-stream", gateway.APIOpenAI)
	out := feedAll(r, chunk("Write to [EM"), chunk("AIL_1] and [EMAIL_2]."), chunk("", "stop"), "data: [DONE]\n\n")
	got := contents(t, strings.Join(out, ""))
	want := []string{"Write to ", "a@b.com and c@d.org.", "<stop>", "[DONE]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Transport chunks don't follow events: one event can arrive in pieces, or
// several in one.
func TestSSEEventSplitAcrossTransportChunks(t *testing.T) {
	r := newBodyRehydrator(vault(t), "text/event-stream", gateway.APIOpenAI)
	all := chunk("Hi [EMAIL_1]") + chunk(" bye", "stop") + "data: [DONE]\n\n"
	out := feedAll(r, all[:17], all[17:60], all[60:])
	if out[0] != "" {
		t.Fatalf("emitted half an event: %q", out[0])
	}
	got := contents(t, strings.Join(out, ""))
	if strings.Join(got, "|") != "Hi a@b.com| bye<stop>|[DONE]" {
		t.Fatalf("got %q", got)
	}
}

// A tail still held when the choice finishes goes out with the finishing
// event, as written: it never became a placeholder.
func TestSSEHeldTailFlushedAtFinish(t *testing.T) {
	r := newBodyRehydrator(vault(t), "text/event-stream", gateway.APIOpenAI)
	out := feedAll(r, chunk("almost [EMAIL_"), chunk("", "length"), "data: [DONE]\n\n")
	got := contents(t, strings.Join(out, ""))
	if strings.Join(got, "|") != "almost |[EMAIL_<length>|[DONE]" {
		t.Fatalf("got %q", got)
	}
}

// A stream that ends without a finish_reason still gets its held tail, in an
// event of its own before [DONE] (or at the very end).
func TestSSEHeldTailFlushedBeforeDone(t *testing.T) {
	r := newBodyRehydrator(vault(t), "text/event-stream", gateway.APIOpenAI)
	out := feedAll(r, chunk("cut at [EM"), "data: [DONE]\n\n")
	if got := contents(t, strings.Join(out, "")); strings.Join(got, "|") != "cut at |[EM|[DONE]" {
		t.Fatalf("got %q", got)
	}
	r = newBodyRehydrator(vault(t), "text/event-stream", gateway.APIOpenAI)
	out = feedAll(r, chunk("cut at [EM"), "")
	if got := contents(t, strings.Join(out, "")); strings.Join(got, "|") != "cut at |[EM" {
		t.Fatalf("got %q", got)
	}
}

// Events with nothing to restore pass as sent, comments and all.
func TestSSEUntouchedEventsPassAsSent(t *testing.T) {
	r := newBodyRehydrator(vault(t), "text/event-stream", gateway.APIOpenAI)
	in := ": keep-alive\n\n" + chunk("plain") + "data: [DONE]\n\n"
	if out := strings.Join(feedAll(r, in[:5], in[5:]), ""); out != in {
		t.Fatalf("got %q", out)
	}
}

func TestOtherContentTypesAreNotRehydrated(t *testing.T) {
	if newBodyRehydrator(vault(t), "text/plain", gateway.APIOpenAI) != nil {
		t.Fatal("text/plain rehydrated")
	}
}

// fakeStream plays one request and its response through Process.
type fakeStream struct {
	grpc.ServerStream
	in  []*extprocv3.ProcessingRequest
	out []*extprocv3.ProcessingResponse
}

func (f *fakeStream) Recv() (*extprocv3.ProcessingRequest, error) {
	if len(f.in) == 0 {
		return nil, io.EOF
	}
	r := f.in[0]
	f.in = f.in[1:]
	return r, nil
}

func (f *fakeStream) Send(r *extprocv3.ProcessingResponse) error {
	f.out = append(f.out, r)
	return nil
}

func headerMsg(resp bool, kv ...string) *extprocv3.ProcessingRequest {
	hm := &corev3.HeaderMap{}
	for i := 0; i+1 < len(kv); i += 2 {
		hm.Headers = append(hm.Headers, &corev3.HeaderValue{Key: kv[i], RawValue: []byte(kv[i+1])})
	}
	if resp {
		return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{ResponseHeaders: &extprocv3.HttpHeaders{Headers: hm}}}
	}
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{Headers: hm}}}
}

func bodyMsg(resp bool, b string, end bool) *extprocv3.ProcessingRequest {
	if resp {
		return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: &extprocv3.HttpBody{Body: []byte(b), EndOfStream: end}}}
	}
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{Body: []byte(b), EndOfStream: end}}}
}

func play(t *testing.T, user string, respHeaders []string, respChunks ...string) *fakeStream {
	t.Helper()
	snap := gateway.DemoSnapshot()
	k := snap.KeyByID("k1")
	f := &fakeStream{in: []*extprocv3.ProcessingRequest{
		headerMsg(false, "x-stargate-key-id", k.ID, "x-stargate-team", k.Team, "x-stargate-project", k.Project, "x-request-id", "req-1"),
		bodyMsg(false, string(body("gpt-5-mini", user)), true),
		headerMsg(true, respHeaders...),
	}}
	for i, c := range respChunks {
		f.in = append(f.in, bodyMsg(true, c, i == len(respChunks)-1))
	}
	if err := newServer(snap).Process(f); err != nil {
		t.Fatal(err)
	}
	if len(f.out) != 3+len(respChunks) {
		t.Fatalf("%d responses for %d messages", len(f.out), 3+len(respChunks))
	}
	return f
}

// chunkOut is what Envoy sends on for a response body chunk.
func chunkOut(t *testing.T, r *extprocv3.ProcessingResponse, in string) string {
	t.Helper()
	if r.GetResponseBody() == nil {
		t.Fatalf("not a response body answer: %v", r)
	}
	c := r.GetResponseBody().GetResponse()
	switch m := c.GetBodyMutation().GetMutation().(type) {
	case nil:
		return in
	case *extprocv3.BodyMutation_ClearBody:
		return ""
	case *extprocv3.BodyMutation_Body:
		return string(m.Body)
	default:
		t.Fatalf("mutation %T", m)
	}
	return ""
}

// End to end through the ext_proc stream: the request is redacted, the
// response restored, content-length dropped since the length changes, and
// the receipt metadata says how many came back.
func TestProcessRehydratesTheResponseOnTheSameStream(t *testing.T) {
	resp := `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello [EMAIL_1]"}}]}`
	f := play(t, "mail a@b.com", []string{"content-type", "application/json", "content-length", "80"}, resp[:30], resp[30:])
	b, _ := mutated(f.out[1])
	if c := b["messages"].([]any)[1].(map[string]any)["content"]; c != "mail [EMAIL_1]" {
		t.Fatalf("request content = %v", c)
	}
	hm := f.out[2].GetResponseHeaders().GetResponse().GetHeaderMutation()
	if hm.GetRemoveHeaders() == nil || hm.GetRemoveHeaders()[0] != "content-length" {
		t.Fatalf("response headers mutation = %v", hm)
	}
	if got := chunkOut(t, f.out[3], resp[:30]) + chunkOut(t, f.out[4], resp[30:]); !strings.Contains(got, `"content":"Hello a@b.com"`) {
		t.Fatalf("caller gets %s", got)
	}
	p := policyOf(t, f.out[4])
	if len(p.Redactions) != 1 || p.Redactions[0].Type != "email" || p.Redactions[0].Count != 1 || p.Redactions[0].Rehydrated != 1 {
		t.Fatalf("policy redactions = %+v", p.Redactions)
	}
}

func TestProcessRehydratesAStream(t *testing.T) {
	chunks := []string{chunk("Hi [EM"), chunk("AIL_1]!"), chunk("", "stop") + "data: [DONE]\n\n"}
	f := play(t, "mail a@b.com", []string{"content-type", "text/event-stream"}, chunks...)
	var out strings.Builder
	for i, c := range chunks {
		out.WriteString(chunkOut(t, f.out[3+i], c))
	}
	if got := contents(t, out.String()); strings.Join(got, "|") != "Hi |a@b.com!|<stop>|[DONE]" {
		t.Fatalf("caller gets %q", got)
	}
}

// Nothing to restore: Warden asks Envoy not to send the body at all, and
// passes it through untouched if it comes anyway.
func TestProcessWithoutVaultSkipsTheResponseBody(t *testing.T) {
	resp := `{"choices":[{"index":0,"message":{"content":"[EMAIL_1]"}}]}`
	f := play(t, "hello", []string{"content-type", "application/json", "content-length", "60"}, resp)
	h := f.out[2]
	if h.GetModeOverride().GetResponseBodyMode() != procmodev3.ProcessingMode_NONE {
		t.Fatalf("mode override = %v", h.GetModeOverride())
	}
	if h.GetResponseHeaders().GetResponse().GetHeaderMutation() != nil {
		t.Fatalf("headers mutated: %v", h.GetResponseHeaders())
	}
	if got := chunkOut(t, f.out[3], resp); got != resp {
		t.Fatalf("body changed: %s", got)
	}
}

// A compressed body can't be read, so it isn't touched; the receipt says the
// placeholders weren't restored.
func TestProcessLeavesAnEncodedBodyAlone(t *testing.T) {
	resp := "\x1f\x8b binary"
	f := play(t, "mail a@b.com", []string{"content-type", "application/json", "content-encoding", "gzip"}, resp)
	h := f.out[2]
	if h.GetResponseHeaders().GetResponse().GetHeaderMutation() != nil || h.GetModeOverride().GetResponseBodyMode() != procmodev3.ProcessingMode_NONE {
		t.Fatalf("headers answer = %v", h)
	}
	if got := chunkOut(t, f.out[3], resp); got != resp {
		t.Fatalf("body changed: %q", got)
	}
	p := policyOf(t, h)
	if p.Redactions[0].Rehydrated != 0 || !strings.Contains(p.Trace[len(p.Trace)-1].Outcome, "gzip") {
		t.Fatalf("policy = %+v", p)
	}
}
