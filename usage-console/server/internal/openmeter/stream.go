package openmeter

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// streamLimit is how many of the newest receipts each poll compares.
const streamLimit = 100

// streamTraffic is Traffic's live stream. Nothing pushes usage here, so it
// polls OpenMeter and sends each receipt that is new, or changed since it
// was sent: a request's token events can land after the request's own.
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

	ctx := r.Context()
	var sent streamed
	first := true
	poll := time.NewTicker(s.poll())
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		rs, err := s.listReceipts(ctx, streamLimit)
		if err != nil && ctx.Err() == nil {
			slog.Error("stream: read receipts", "err", err)
		}
		if err == nil {
			for _, c := range sent.update(rs) {
				if !first {
					fmt.Fprintf(w, "event: receipt\nid: %s\ndata: %s\n\n", c.id, c.data)
				}
			}
		}
		if first {
			// What the stream started from; a comment, which EventSource ignores.
			fmt.Fprint(w, ": ready\n\n")
			first = false
		}
		flusher.Flush()
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-poll.C:
		}
	}
}

// streamed is what one stream has sent: each receipt's JSON by ID, for the
// receipts of the latest read only. A receipt that has left the newest
// streamLimit is never read again, so it is forgotten; the stream's memory
// stays at one read however long it is open.
type streamed struct{ sent map[string]string }

type streamChange struct {
	id   string
	data []byte
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
			out = append(out, streamChange{rs[i].ID, b})
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
