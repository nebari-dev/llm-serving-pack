package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Server serves POST /v1/chat/completions and writes a receipt per request.
type Server struct {
	Snap  *Current
	Up    Upstream
	Store *store.Store
	// throttle counts requests per key across requests (Input.Throttle).
	throttle Throttle
}

func (g *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.chat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func (g *Server) save(ctx context.Context, rc *model.Receipt) {
	// Receipts must outlive a client that hung up mid-stream.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := g.Store.PutReceipt(ctx, rc); err != nil {
		log.Printf("receipt %s: %v", rc.ID, err)
	}
}

func (g *Server) chat(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	if err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	var cr fakellm.ChatRequest
	if err := json.Unmarshal(body, &cr); err != nil {
		writeErr(w, 400, "bad_request", "invalid JSON body")
		return
	}
	snap := g.Snap.Load()
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	in := Input{
		Secret: req.Header.Get("Authorization"), Region: req.Header.Get("X-Data-Region"),
		SessionID: req.Header.Get("X-Session-Id"), Actor: req.Header.Get("X-Actor"),
		Req: cr, Body: body, Now: time.Now(), Throttle: &g.throttle,
	}
	d := Admit(snap, in, r)
	if d.Reject != nil {
		if rc := d.Finish(snap, nil, Result{}, nil, time.Now()); rc != nil {
			g.save(req.Context(), rc)
			w.Header().Set("X-Stargate-Receipt", rc.ID)
		}
		if d.Reject.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(d.Reject.RetryAfter))
		}
		writeErr(w, d.Reject.Status, d.Reject.Code, d.Reject.Message)
		return
	}
	w.Header().Set("X-Stargate-Receipt", d.Receipt.ID)

	if !cr.Stream {
		c, res, failed := Execute(req.Context(), d, g.Up, nil)
		rc := d.Finish(snap, c, res, failed, time.Now())
		g.save(req.Context(), rc)
		if res.Status != 200 {
			writeErr(w, res.Status, rc.ErrorCode, rc.ErrorDetail)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-" + rc.ID, "object": "chat.completion", "created": in.Now.Unix(), "model": rc.ResolvedModel,
			"choices": []any{map[string]any{"index": 0, "finish_reason": finishReason(res),
				"message": map[string]any{"role": "assistant", "content": res.Content}}},
			"usage": res.Usage,
		})
		return
	}

	// Streaming: the receipt appears in flight right away and settles when the
	// stream ends (tokens arrive last, §13).
	flight := *d.Receipt
	flight.InFlight, flight.Status = true, 200
	if b, ok := snap.primary(d.Req.Model); ok {
		flight.ResolvedModel, flight.Backend, flight.Provider, flight.Region = d.Req.Model, b.Name, b.Provider, b.Region
	}
	flight.Trace = []model.TraceStep{d.identity, d.budgetStep, d.rulesStep}
	g.save(req.Context(), &flight)

	flusher, _ := w.(http.Flusher)
	started := false
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	id := "chatcmpl-" + d.Receipt.ID
	c, res, failed := Execute(req.Context(), d, g.Up, func(delta string) bool {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			started = true
		}
		send(map[string]any{"id": id, "object": "chat.completion.chunk", "model": d.Req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta}}}})
		return req.Context().Err() == nil
	})
	rc := d.Finish(snap, c, res, failed, time.Now())
	g.save(req.Context(), rc)
	if !started {
		if res.Status != 200 {
			writeErr(w, res.Status, rc.ErrorCode, rc.ErrorDetail)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
	}
	send(map[string]any{"id": id, "object": "chat.completion.chunk", "model": rc.ResolvedModel,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason(res)}},
		"usage":   res.Usage})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func finishReason(res Result) string {
	if res.Truncated {
		return "content_filter"
	}
	return "stop"
}
