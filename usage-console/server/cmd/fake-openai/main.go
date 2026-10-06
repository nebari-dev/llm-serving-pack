// fake-openai serves OpenAI-compatible chat completions for every demo
// backend at /{backend}/v1/chat/completions, sleeping through the simulated
// latency and streaming chunks when asked. With an X-Fake-Echo header it
// repeats the prompt instead (fakellm.Echo). X-Fake-Reasoning: n adds n
// reasoning tokens counted inside completion_tokens, as OpenAI reports them
// (fakellm.OpenAIReasoning). Like a real provider, it rejects a
// Stargate API key: the gateway must never forward the caller's credentials.
// GET /{backend}/v1/models lists a backend's models. The "keyed" backend
// (fakellm.KeyedBackend) wants its own provider key, as a real provider does,
// answers 401 without it, and echoes the prompt, saying which of its two keys
// it got in X-Fake-Key. Every backend also answers Anthropic's Messages API
// at POST /{backend}/v1/messages (streamed as Anthropic's events), for
// Anthropic-style callers the gateway sends to an Anthropic backend natively.
// The "keyed-anthropic" backend (fakellm.AnthropicBackend) is Anthropic as
// the console adds it: GET /{backend}/v1/models and POST /{backend}/v1/messages
// with x-api-key and anthropic-version, and its OpenAI-compatible chat
// completions with the same key as a bearer token, both echoing the prompt.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/fakellm"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{backend}/v1/chat/completions", handle)
	mux.HandleFunc("GET /{backend}/v1/models", listModels)
	mux.HandleFunc("POST /{backend}/v1/messages", messages)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	log.Printf("fake-openai listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// authorized answers 401 for a backend whose provider key the request lacks.
func authorized(w http.ResponseWriter, req *http.Request) bool {
	if fakellm.Authorized(req.PathValue("backend"), req.Header.Get("Authorization")) {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(fakellm.Unauthorized))
	return false
}

// anthropicAuthorized answers as Anthropic does for a request without its key.
func anthropicAuthorized(w http.ResponseWriter, req *http.Request) bool {
	status, body := fakellm.AnthropicAuth(req.Header.Get("X-Api-Key"), req.Header.Get("Anthropic-Version"))
	if status == 0 {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(body))
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// stargateKeyLeaked answers 401 when the caller's Stargate key reached the
// provider: the gateway must never forward it.
func stargateKeyLeaked(w http.ResponseWriter, req *http.Request) bool {
	for _, h := range []string{"Authorization", "X-Api-Key"} {
		if strings.Contains(req.Header.Get(h), "ngw_") {
			log.Printf("%s: caller's Stargate key reached the provider in %s", req.URL.Path, h)
			http.Error(w, `{"error":{"message":"a Stargate API key reached the provider","code":401}}`, http.StatusUnauthorized)
			return true
		}
	}
	return false
}

// messages is Anthropic's Messages API. The Anthropic backend wants its key
// as x-api-key and echoes the prompt, saying what arrived in X-Fake-Received
// as chat completions do; the rest simulate their profile (anthropic-prod is
// an Anthropic backend, which Anthropic-style callers reach natively) and
// echo with X-Fake-Echo. Streams follow Anthropic's events.
func messages(w http.ResponseWriter, req *http.Request) {
	if stargateKeyLeaked(w, req) {
		return
	}
	backend := req.PathValue("backend")
	if fakellm.IsAnthropic(backend) && !anthropicAuthorized(w, req) {
		return
	}
	var mr fakellm.AnthropicRequest
	if err := json.NewDecoder(req.Body).Decode(&mr); err != nil || mr.Model == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"invalid JSON, or no model"}}`))
		return
	}
	cr := fakellm.AnthropicChat(mr)
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	p := fakellm.Simulate(backend, cr, r)
	if req.Header.Get("X-Fake-Echo") != "" || fakellm.IsAnthropic(backend) {
		p = fakellm.Echo(cr)
		w.Header().Set("X-Fake-Received", url.QueryEscape(p.Content()))
	}
	ctx := req.Context()
	sleep := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-ctx.Done():
			return false
		}
	}
	if p.Status != 200 {
		sleep(p.Duration)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(p.Status)
		w.Write([]byte(fakellm.AnthropicFailure(p)))
		return
	}
	id := fmt.Sprintf("msg_fake%016x", r.Uint64())
	if !mr.Stream {
		if sleep(p.Duration) {
			writeJSON(w, fakellm.AnthropicMessage(id, mr.Model, p))
		}
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if !sleep(p.TTFT) {
		return
	}
	gap := (p.Duration - p.TTFT) / time.Duration(max(len(p.Chunks), 1))
	for _, e := range fakellm.AnthropicEvents(id, mr.Model, p) {
		if e.Name == "content_block_delta" && !sleep(gap) {
			return
		}
		b, _ := json.Marshal(e.Data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, b)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func listModels(w http.ResponseWriter, req *http.Request) {
	if fakellm.IsAnthropic(req.PathValue("backend")) {
		if anthropicAuthorized(w, req) {
			writeJSON(w, fakellm.AnthropicModels())
		}
		return
	}
	if !authorized(w, req) {
		return
	}
	var data []any
	for _, m := range fakellm.ModelsFor(req.PathValue("backend")) {
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "fake-openai"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func handle(w http.ResponseWriter, req *http.Request) {
	if stargateKeyLeaked(w, req) {
		return
	}
	if !authorized(w, req) {
		return
	}
	var cr fakellm.ChatRequest
	if err := json.NewDecoder(req.Body).Decode(&cr); err != nil {
		http.Error(w, `{"error":{"message":"invalid json"}}`, http.StatusBadRequest)
		return
	}
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	p := fakellm.Simulate(req.PathValue("backend"), cr, r)
	if req.Header.Get("X-Fake-Echo") != "" || req.PathValue("backend") == fakellm.KeyedBackend || fakellm.IsAnthropic(req.PathValue("backend")) {
		// For checks through the gateway: reply with the prompt as it arrived,
		// and say so in a header too, which nothing on the way back rewrites
		// (Warden restores redacted values in the body).
		p = fakellm.Echo(cr)
		w.Header().Set("X-Fake-Received", url.QueryEscape(p.Content()))
		if n := fakellm.KeyedKeyNumber(req.Header.Get("Authorization")); n > 0 {
			w.Header().Set("X-Fake-Key", strconv.Itoa(n))
		}
	}
	if n, err := strconv.Atoi(req.Header.Get("X-Fake-Reasoning")); err == nil && n > 0 && p.Status == 200 {
		// As a real reasoning model reports it: inside completion_tokens.
		p.Usage = fakellm.OpenAIReasoning(p.Usage, n)
	}
	ctx := req.Context()
	sleep := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-ctx.Done():
			return false
		}
	}

	id := fmt.Sprintf("chatcmpl-%016x", r.Uint64())
	if p.Status != 200 {
		sleep(p.Duration)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(p.Status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": p.Error, "code": p.Status}})
		return
	}

	if !cr.Stream {
		if !sleep(p.Duration) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": cr.Model,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": p.Content()}}},
			"usage": p.Usage,
		})
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if !sleep(p.TTFT) {
		return
	}
	gap := (p.Duration - p.TTFT) / time.Duration(max(len(p.Chunks), 1))
	for i, c := range p.Chunks {
		if i > 0 && !sleep(gap) {
			return
		}
		send(map[string]any{"id": id, "object": "chat.completion.chunk", "model": cr.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": c}}}})
	}
	send(map[string]any{"id": id, "object": "chat.completion.chunk", "model": cr.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	if cr.StreamOptions != nil && cr.StreamOptions.IncludeUsage {
		// As OpenAI sends it: a chunk of its own with no choices. Agent
		// Router's Messages translation reads usage only from that.
		send(map[string]any{"id": id, "object": "chat.completion.chunk", "model": cr.Model, "choices": []any{}, "usage": p.Usage})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
