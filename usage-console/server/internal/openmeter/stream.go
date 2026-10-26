package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// streamLimit is how many of the newest receipts each poll compares.
const streamLimit = 100

// streamBuffer is how many changes a stream may fall behind by before the
// newest are dropped (and the console told how many).
const streamBuffer = 256

// streamTraffic is Traffic's live stream, with Traffic's filters. Nothing
// pushes usage here, so one poller (the Server's hub) reads OpenMeter for
// every open stream and sends each its matching receipts that are new, or
// changed since they were sent: a request's token events can land after the
// request's own.
func (s *Server) streamTraffic(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	sub := s.streams().subscribe(filterOf(r.URL.Query()))
	defer s.streams().unsubscribe(sub)
	ctx := r.Context()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	select {
	case <-ctx.Done():
		return
	case <-sub.ready:
	}
	// What the stream started from; a comment, which EventSource ignores.
	fmt.Fprint(w, ": ready\n\n")
	flusher.Flush()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
		case c := <-sub.changes:
			fmt.Fprintf(w, "event: receipt\nid: %s\ndata: %s\n\n", c.id, c.data)
		}
		if n := sub.takeDropped(); n > 0 {
			fmt.Fprintf(w, "event: dropped\ndata: {\"count\":%d}\n\n", n)
		}
		flusher.Flush()
	}
}

func (s *Server) streams() *hub {
	s.hubOnce.Do(func() {
		s.hub = &hub{every: s.poll(), read: func(ctx context.Context) ([]model.Receipt, error) {
			return s.listReceipts(ctx, streamLimit, receiptFilter{})
		}}
	})
	return s.hub
}

// hub polls OpenMeter while any stream is open, once per poll however many
// are, and hands each stream the changes its filter matches.
type hub struct {
	every time.Duration
	read  func(context.Context) ([]model.Receipt, error)

	mu   sync.Mutex
	subs map[*subscriber]struct{}
	// stop ends the running poller; nil while none runs.
	stop context.CancelFunc
	// baselined: the running poller has made its first read, so a stream
	// that joins now gets everything that changes after it joined.
	baselined bool
}

type subscriber struct {
	filter  receiptFilter
	changes chan streamChange
	ready   chan struct{}
	mu      sync.Mutex
	dropped int
}

func (s *subscriber) takeDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.dropped
	s.dropped = 0
	return n
}

func (h *hub) subscribe(f receiptFilter) *subscriber {
	sub := &subscriber{filter: f, changes: make(chan streamChange, streamBuffer), ready: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[*subscriber]struct{}{}
	}
	h.subs[sub] = struct{}{}
	if h.baselined {
		close(sub.ready)
	}
	if h.stop == nil {
		ctx, stop := context.WithCancel(context.Background())
		h.stop = stop
		go h.run(ctx)
	}
	return sub
}

func (h *hub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub)
	if len(h.subs) == 0 && h.stop != nil {
		h.stop()
		h.stop = nil
		h.baselined = false
	}
}

// run polls until stopped. Its first read is the baseline: what was there
// before any stream joined is not sent.
func (h *hub) run(ctx context.Context) {
	var sent streamed
	tick := time.NewTicker(h.every)
	defer tick.Stop()
	for {
		rs, err := h.read(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("stream: read receipts", "err", err)
		}
		var changes []streamChange
		if err == nil {
			changes = sent.update(rs)
		}
		h.mu.Lock()
		if ctx.Err() != nil {
			h.mu.Unlock()
			return
		}
		for sub := range h.subs {
			if !h.baselined {
				select {
				case <-sub.ready:
				default:
					close(sub.ready)
				}
				continue
			}
			for _, c := range changes {
				if !sub.filter.matchReceipt(c.receipt) {
					continue
				}
				select {
				case sub.changes <- c:
				default:
					sub.mu.Lock()
					sub.dropped++
					sub.mu.Unlock()
				}
			}
		}
		h.baselined = true
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// streamed is what the hub has sent: each receipt's JSON by ID, for the
// receipts of the latest read only. A receipt that has left the newest
// streamLimit is never read again, so it is forgotten; the memory stays at
// one read however long streams are open.
type streamed struct{ sent map[string]string }

type streamChange struct {
	id      string
	data    []byte
	receipt model.Receipt
}

// update takes the newest receipts (newest first) and returns those that are
// new or changed since they were sent, oldest first, so the console sees
// them in order.
func (s *streamed) update(rs []model.Receipt) []streamChange {
	next := make(map[string]string, len(rs))
	var out []streamChange
	for i := len(rs) - 1; i >= 0; i-- {
		b, _ := json.Marshal(rs[i])
		next[rs[i].ID] = string(b)
		if s.sent[rs[i].ID] != string(b) {
			out = append(out, streamChange{rs[i].ID, b, rs[i]})
		}
	}
	s.sent = next
	return out
}

func (s *Server) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 5 * time.Second
}
