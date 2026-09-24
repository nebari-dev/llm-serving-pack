package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Hub fans receipts out to SSE subscribers. It LISTENs on the receipts db, so
// any writer (the dev gateway today, the OTel collector later) reaches the
// console without talking to the API.
type Hub struct {
	mu   sync.Mutex
	subs map[chan model.Receipt]filter
}

type filter struct {
	tenant                             string
	key, team, model, verdict, backend string
}

func (f filter) match(r model.Receipt) bool {
	return r.TenantID == f.tenant &&
		(f.key == "" || r.KeyID == f.key) &&
		(f.team == "" || r.Team == f.team) &&
		(f.model == "" || r.ResolvedModel == f.model || r.RequestedModel == f.model) &&
		(f.verdict == "" || r.Verdict == f.verdict) &&
		(f.backend == "" || r.Backend == f.backend)
}

func NewHub() *Hub { return &Hub{subs: map[chan model.Receipt]filter{}} }

func (h *Hub) subscribe(f filter) chan model.Receipt {
	ch := make(chan model.Receipt, 256)
	h.mu.Lock()
	h.subs[ch] = f
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan model.Receipt) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) publish(r model.Receipt) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, f := range h.subs {
		if !f.match(r) {
			continue
		}
		select {
		case ch <- r:
		default: // slow consumer: drop rather than stall everyone
		}
	}
}

// Listen relays NOTIFYs from the receipts db until ctx ends, reconnecting on
// failure.
func (h *Hub) Listen(ctx context.Context, st *store.Store, pool *pgxpool.Pool) {
	for ctx.Err() == nil {
		if err := h.listenOnce(ctx, st, pool); err != nil && ctx.Err() == nil {
			log.Printf("receipt listener: %v; retrying", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context, st *store.Store, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+store.NotifyChannel); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		id, tsStr, _ := strings.Cut(n.Payload, " ")
		ts, _ := strconv.ParseInt(tsStr, 10, 64)
		// The payload carries no tenant, so fetch across tenants by id + ts.
		r, err := st.ReceiptAnyTenant(ctx, id, ts)
		if err != nil {
			log.Printf("receipt %s: %v", id, err)
			continue
		}
		h.publish(r)
	}
}

// streamTraffic is GET /stream/traffic: one "receipt" event per insert or
// settle, filtered server side by key, team, model, verdict and backend.
func (s *Server) streamTraffic(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	q := r.URL.Query()
	ch := s.Hub.subscribe(filter{tenant: t, key: q.Get("key"), team: q.Get("team"), model: q.Get("model"), verdict: q.Get("verdict"), backend: q.Get("backend")})
	defer s.Hub.unsubscribe(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil, nil
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
		case rc := <-ch:
			b, _ := json.Marshal(rc)
			fmt.Fprintf(w, "event: receipt\nid: %s\ndata: %s\n\n", rc.ID, b)
		}
		flusher.Flush()
	}
}
