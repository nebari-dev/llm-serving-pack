package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	subs map[*subscriber]struct{}
}

// subscriber is one SSE connection. A slow one misses receipts rather than
// stalling everyone, and dropped counts them so the stream can say so (§7.5.3:
// never silently drop). Above sampleThreshold it's sampled, and says so.
type subscriber struct {
	ch      chan model.Receipt
	f       filter
	dropped atomic.Int64
	sample  *sampler // guarded by Hub.mu
}

// §7.5.3 backpressure. Above sampleThreshold matching requests a second a
// connection gets 1 in N of them, N the smallest round number that brings it
// back under, and a "sampling" event saying so. The threshold is what the
// Traffic table was measured to hold (2,500 rows/min at p95 17ms a frame,
// docs/console-real-data.md), so sampling starts where the console would
// start to stutter, not earlier. The rate is measured per connection after
// its filters, so adding a filter is how to see everything matching.
const (
	sampleThreshold = 40.0 // matching requests a second (2,400 a minute)
	sampleExit      = 30.0 // sampling ends below this, so a rate at the threshold doesn't flap
	sampleWindow    = 5 * time.Second
)

// sampleSteps are the N in "1 in N": round numbers people read at a glance.
var sampleSteps = []int{2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

// oneIn is the N that brings perSec under sampleThreshold (1 if it is).
func oneIn(perSec float64) int {
	if perSec <= sampleThreshold {
		return 1
	}
	for _, n := range sampleSteps {
		if perSec/float64(n) <= sampleThreshold {
			return n
		}
	}
	return sampleSteps[len(sampleSteps)-1]
}

// SamplingNotice is the "sampling" event: OneIn 1 means everything is sent.
// RatePerSec is matching requests a second over the last window.
type SamplingNotice struct {
	OneIn           int     `json:"oneIn"`
	RatePerSec      float64 `json:"ratePerSec"`
	ThresholdPerSec float64 `json:"thresholdPerSec"`
}

// sampler decides which matching receipts one connection gets. It keeps a
// receipt by a hash of its id, so the in-flight and settled copies of a
// request agree, and every tab samples the same requests. The rate counts
// settled receipts: each request settles once.
type sampler struct {
	n     int
	rate  float64
	count int
	since time.Time
	sent  map[string]struct{} // in-flight rows sent, whose settle must follow
}

func newSampler(now time.Time) *sampler {
	return &sampler{n: 1, since: now, sent: map[string]struct{}{}}
}

func (s *sampler) admit(r model.Receipt) bool {
	if !r.InFlight {
		s.count++
	}
	if _, ok := s.sent[r.ID]; ok {
		if !r.InFlight {
			delete(s.sent, r.ID)
		}
		return true
	}
	keep := s.n <= 1 || fnv32(r.ID)%uint32(s.n) == 0
	if keep && r.InFlight {
		if len(s.sent) >= 10_000 { // settles that never came (a lost NOTIFY)
			clear(s.sent)
		}
		s.sent[r.ID] = struct{}{}
	}
	return keep
}

// roll closes the window at now: it measures the rate and picks N. changed
// is whether N moved; while sampling every roll is worth sending, for the
// rate.
func (s *sampler) roll(now time.Time) (SamplingNotice, bool) {
	if secs := now.Sub(s.since).Seconds(); secs > 0 {
		s.rate = math.Round(float64(s.count)/secs*10) / 10
	}
	s.count, s.since = 0, now
	n := oneIn(s.rate)
	if s.n > 1 && n == 1 && s.rate >= sampleExit {
		n = 2
	}
	changed := n != s.n
	s.n = n
	return SamplingNotice{OneIn: n, RatePerSec: s.rate, ThresholdPerSec: sampleThreshold}, changed
}

func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

type filter struct {
	tenant string
	q      store.ReceiptQuery
}

func in(vals []string, v string) bool { return len(vals) == 0 || slices.Contains(vals, v) }

func (f filter) match(r model.Receipt) bool {
	q := f.q
	return r.TenantID == f.tenant &&
		in(q.Keys, r.KeyID) && in(q.Teams, r.Team) && in(q.Projects, r.ProjectID) &&
		(len(q.Models) == 0 || slices.Contains(q.Models, r.ResolvedModel) || slices.Contains(q.Models, r.RequestedModel)) &&
		in(q.Verdicts, r.Verdict) && in(q.Providers, r.Provider) && in(q.Backends, r.Backend) &&
		in(q.Reasons, r.RouteReason) && in(q.Sessions, r.SessionID)
}

func NewHub() *Hub { return &Hub{subs: map[*subscriber]struct{}{}} }

func (h *Hub) subscribe(f filter) *subscriber {
	sub := &subscriber{ch: make(chan model.Receipt, 256), f: f, sample: newSampler(time.Now())}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

func (h *Hub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	delete(h.subs, sub)
	h.mu.Unlock()
}

func (h *Hub) publish(r model.Receipt) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if !sub.f.match(r) || !sub.sample.admit(r) {
			continue
		}
		select {
		case sub.ch <- r:
		default: // slow consumer: drop rather than stall everyone, and count it
			sub.dropped.Add(1)
		}
	}
}

// roll closes sub's sampling window (see sampler.roll).
func (h *Hub) roll(sub *subscriber, now time.Time) (SamplingNotice, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return sub.sample.roll(now)
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
// settle, with the same filters as GET /receipts (limit, before, since and
// range are ignored). When receipts were dropped because this connection fell
// behind, a "dropped" event with {"count": n} comes before the next receipt,
// or with the next heartbeat. A "sampling" event (SamplingNotice) says when
// the connection starts or stops being sampled, and while it is, gives the
// rate every few seconds. A new connection starts unsampled.
func (s *Server) streamTraffic(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	sub := s.Hub.subscribe(filter{tenant: t, q: receiptQuery(r)})
	defer s.Hub.unsubscribe(sub)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	reportDrops := func() {
		if n := sub.dropped.Swap(0); n > 0 {
			fmt.Fprintf(w, "event: dropped\ndata: {\"count\":%d}\n\n", n)
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	// The first window is a second long, so a busy stream is sampled before
	// it fills the connection's buffer; then sampleWindow.
	window := time.NewTicker(time.Second)
	defer window.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil, nil
		case now := <-window.C:
			window.Reset(sampleWindow)
			n, changed := s.Hub.roll(sub, now)
			if !changed && n.OneIn == 1 {
				continue
			}
			b, _ := json.Marshal(n)
			fmt.Fprintf(w, "event: sampling\ndata: %s\n\n", b)
		case <-heartbeat.C:
			reportDrops()
			fmt.Fprint(w, ": ping\n\n")
		case rc := <-sub.ch:
			reportDrops()
			b, _ := json.Marshal(rc)
			fmt.Fprintf(w, "event: receipt\nid: %s\ndata: %s\n\n", rc.ID, b)
		}
		flusher.Flush()
	}
}
