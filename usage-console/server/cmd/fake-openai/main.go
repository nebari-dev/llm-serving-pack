// fake-openai serves OpenAI-compatible chat completions for every demo
// backend at /{backend}/v1/chat/completions, sleeping through the simulated
// latency and streaming chunks when asked. Like a real provider, it rejects a
// Stargate API key: the gateway must never forward the caller's credentials.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/fakellm"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{backend}/v1/chat/completions", handle)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	log.Printf("fake-openai listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func handle(w http.ResponseWriter, req *http.Request) {
	for _, h := range []string{"Authorization", "X-Api-Key"} {
		if strings.Contains(req.Header.Get(h), "ngw_") {
			log.Printf("%s: caller's Stargate key reached the provider in %s", req.URL.Path, h)
			http.Error(w, `{"error":{"message":"a Stargate API key reached the provider","code":401}}`, http.StatusUnauthorized)
			return
		}
	}
	var cr fakellm.ChatRequest
	if err := json.NewDecoder(req.Body).Decode(&cr); err != nil {
		http.Error(w, `{"error":{"message":"invalid json"}}`, http.StatusBadRequest)
		return
	}
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	p := fakellm.Simulate(req.PathValue("backend"), cr, r)
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
	final := map[string]any{"id": id, "object": "chat.completion.chunk", "model": cr.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}
	if cr.StreamOptions != nil && cr.StreamOptions.IncludeUsage {
		final["usage"] = p.Usage
	}
	send(final)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
