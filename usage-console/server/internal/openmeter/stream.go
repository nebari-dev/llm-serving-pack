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

// streamWindow is how far back each poll of the live stream reads. A
// request's token events can land after its own, and events reach
// ClickHouse a few seconds after the gateway saw them; both settle well
// inside it.
const streamWindow = 2 * time.Minute

// streamBuffer is how many polls' changes a stream may fall behind by before
// the newest are dropped (and the console told how many).
const streamBuffer = 16

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
		case batch := <-sub.changes:
			for _, c := range batch {
				fmt.Fprintf(w, "event: receipt\nid: %s\ndata: %s\n\n", c.id, c.data)
			}
		}
		if n := sub.takeDropped(); n > 0 {
			fmt.Fprintf(w, "event: dropped\ndata: {\"count\":%d}\n\n", n)
		}
		flusher.Flush()
	}
}

func (s *Server) streams() *hub {
	s.hubOnce.Do(func() {
		r := &streamReader{s: s}
		s.hub = &hub{every: s.poll(), read: r.read}
	})
	return s.hub
}

// streamReader is the hub's read of OpenMeter: every request in the last
// streamWindow, up to receiptScanCap. It looks up tokens and cost only for
// receipts it hasn't settled yet, so a poll costs about one OpenMeter call
// per hundred requests in the window plus the new ones' token lookups.
type streamReader struct {
	s *Server
	// settled are receipts in the window whose tokens are in (or that expect
	// none), by ID.
	settled map[string]model.Receipt
	// missedTo is the newest time (epoch ms) already reported missed, and
	// prevNewest the newest receipt of the previous read: only what fell
	// between two reads is missed, and it is reported once.
	missedTo, prevNewest int64
}

// read returns the window's receipts, newest first, and how many requests
// it had to leave out that it hasn't reported before.
func (sr *streamReader) read(ctx context.Context) ([]model.Receipt, int, error) {
	now := sr.s.Now()
	since := now.Add(-streamWindow).UnixMilli()
	rs, continueBefore, err := sr.s.scanRequests(ctx, receiptScanCap+1, receiptFilter{since: since})
	if err != nil {
		return nil, 0, err
	}
	missed := 0
	if continueBefore > 0 {
		// The scan stopped at receiptScanCap: what's older in the window, the
		// stream can't send. Event times are whole seconds, so the count can
		// be short by the requests of the second the scan stopped in.
		from, to := max(since, sr.missedTo, sr.prevNewest), rs[len(rs)-1].TS
		if from < to {
			rows, err := sr.s.Client.meterQuery(ctx, "llm_requests", time.UnixMilli(from), time.UnixMilli(to), nil, nil)
			if err != nil {
				return nil, 0, err
			}
			for _, row := range rows {
				missed += int(row.Value)
			}
			sr.missedTo = to
		}
	}
	if len(rs) > 0 {
		sr.prevNewest = rs[0].TS
	}
	var open []model.Receipt
	for _, r := range rs {
		if _, ok := sr.settled[r.ID]; !ok {
			open = append(open, r)
		}
	}
	if err := sr.s.complete(ctx, open); err != nil {
		return nil, 0, err
	}
	completed := make(map[string]model.Receipt, len(open))
	for _, r := range open {
		completed[r.ID] = r
	}
	settled := make(map[string]model.Receipt, len(rs))
	for i, r := range rs {
		if done, ok := sr.settled[r.ID]; ok {
			rs[i] = done
		} else {
			rs[i] = completed[r.ID]
		}
		if rs[i].Verdict != "allowed" || rs[i].InputTokens+rs[i].OutputTokens > 0 {
			settled[r.ID] = rs[i]
		}
	}
	sr.settled = settled
	return rs, missed, nil
}

// hub polls OpenMeter while any stream is open, once per poll however many
// are, and hands each stream the changes its filter matches.
type hub struct {
	every time.Duration
	// read returns the receipts to compare, newest first, and how many
	// requests it couldn't read (every stream is told).
	read func(context.Context) ([]model.Receipt, int, error)

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
	changes chan []streamChange
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
	sub := &subscriber{filter: f, changes: make(chan []streamChange, streamBuffer), ready: make(chan struct{})}
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

// run polls until stopped. Its first good read is the baseline: what was
// there before any stream joined is not sent.
func (h *hub) run(ctx context.Context) {
	var sent streamed
	tick := time.NewTicker(h.every)
	defer tick.Stop()
	for {
		rs, missed, err := h.read(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// Nothing to compare against: the next good read decides.
			slog.Error("stream: read receipts", "err", err)
			if !h.wait(ctx, tick) {
				return
			}
			continue
		}
		changes := sent.update(rs)
		h.mu.Lock()
		if ctx.Err() != nil {
			h.mu.Unlock()
			return
		}
		for sub := range h.subs {
			if missed > 0 && h.baselined {
				sub.mu.Lock()
				sub.dropped += missed
				sub.mu.Unlock()
			}
			if !h.baselined {
				select {
				case <-sub.ready:
				default:
					close(sub.ready)
				}
				continue
			}
			var batch []streamChange
			for _, c := range changes {
				if sub.filter.matchReceipt(c.receipt) {
					batch = append(batch, c)
				}
			}
			if len(batch) == 0 {
				continue
			}
			select {
			case sub.changes <- batch:
			default:
				sub.mu.Lock()
				sub.dropped += len(batch)
				sub.mu.Unlock()
			}
		}
		h.baselined = true
		h.mu.Unlock()
		if !h.wait(ctx, tick) {
			return
		}
	}
}

// wait blocks until the next poll, or returns false once stopped.
func (h *hub) wait(ctx context.Context, tick *time.Ticker) bool {
	select {
	case <-ctx.Done():
		return false
	case <-tick.C:
		return true
	}
}

// streamed is what the hub has sent: each receipt's JSON by ID, for the
// receipts of the latest read only. A receipt that has left the window is
// never read again, so it is forgotten; the memory stays at one read however
// long streams are open.
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
