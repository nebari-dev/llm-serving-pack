package api

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestHubCountsDropsForSlowSubscribers(t *testing.T) {
	h := NewHub()
	sub := h.subscribe(filter{tenant: "demo"})
	for i := 0; i < cap(sub.ch)+5; i++ {
		h.publish(model.Receipt{TenantID: "demo"})
	}
	if got := sub.dropped.Load(); got != 5 {
		t.Fatalf("dropped = %d, want 5", got)
	}
}

func TestFilterMatchesAnyValue(t *testing.T) {
	f := filter{tenant: "demo", q: store.ReceiptQuery{Verdicts: []string{"blocked", "redacted"}, Models: []string{"sonnet-5"}}}
	for _, c := range []struct {
		r    model.Receipt
		want bool
	}{
		{model.Receipt{TenantID: "demo", Verdict: "blocked", RequestedModel: "sonnet-5", ResolvedModel: "opus-4-1"}, true},
		{model.Receipt{TenantID: "demo", Verdict: "allowed", ResolvedModel: "sonnet-5"}, false},
		{model.Receipt{TenantID: "other", Verdict: "blocked", ResolvedModel: "sonnet-5"}, false},
	} {
		if got := f.match(c.r); got != c.want {
			t.Errorf("match(%+v) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestReceiptQueryRangeStartsOnABucket(t *testing.T) {
	q := receiptQuery(httptest.NewRequest("GET", "/receipts?range=1h&key=k1&key=k2", nil))
	if q.Since%(5*60_000) != 0 {
		t.Fatalf("since %d isn't on a 5-minute boundary", q.Since)
	}
	if age := time.Since(time.UnixMilli(q.Since)); age < time.Hour || age > time.Hour+5*time.Minute {
		t.Fatalf("since is %v ago, want 1h to 1h05m", age)
	}
	if len(q.Keys) != 2 || !q.Aggregable() {
		t.Fatalf("keys %v aggregable %v", q.Keys, q.Aggregable())
	}
}

// Traffic's project filter takes project ids, like the list (§5.1).
func TestFilterMatchesProjectsByID(t *testing.T) {
	f := filter{tenant: "demo", q: store.ReceiptQuery{Projects: []string{"p1"}}}
	if !f.match(model.Receipt{TenantID: "demo", Project: "helpdesk", ProjectID: "p1"}) {
		t.Error("same id didn't match")
	}
	if f.match(model.Receipt{TenantID: "demo", Project: "helpdesk", ProjectID: "p2"}) {
		t.Error("another team's helpdesk matched")
	}
}

// sampled feeds n settled receipts with distinct ids through s, as one
// window's traffic, and returns how many it let through.
func sampled(s *sampler, prefix string, n int) int {
	kept := 0
	for i := range n {
		if s.admit(model.Receipt{ID: fmt.Sprintf("%s-%d", prefix, i)}) {
			kept++
		}
	}
	return kept
}

// §7.5.3 backpressure: under the threshold every matching receipt goes out.
func TestSamplerSendsEverythingUnderTheThreshold(t *testing.T) {
	t0 := time.Unix(1_000, 0)
	s := newSampler(t0)
	if got := sampled(s, "a", 150); got != 150 {
		t.Fatalf("kept %d of 150 before any window closed", got)
	}
	// 150 in 5s is 30/s, under 40/s.
	if n, changed := s.roll(t0.Add(sampleWindow)); changed || n.OneIn != 1 {
		t.Fatalf("roll = %+v changed %v, want 1 in 1, unchanged", n, changed)
	}
	if got := sampled(s, "b", 100); got != 100 {
		t.Fatalf("kept %d of 100 under the threshold", got)
	}
}

// Above it the stream sends 1 in N, N a round number that brings the rate
// under the threshold, and says so.
func TestSamplerSamplesOneInNAboveTheThreshold(t *testing.T) {
	t0 := time.Unix(1_000, 0)
	s := newSampler(t0)
	sampled(s, "a", 4_000) // 800/s over 5s
	n, changed := s.roll(t0.Add(sampleWindow))
	if !changed || n.OneIn != 20 || n.RatePerSec != 800 || n.ThresholdPerSec != sampleThreshold {
		t.Fatalf("roll = %+v changed %v, want 1 in 20 at 800/s", n, changed)
	}
	kept := sampled(s, "b", 20_000)
	if kept < 800 || kept > 1_200 {
		t.Fatalf("kept %d of 20,000 at 1 in 20, want about 1,000", kept)
	}
	for _, c := range []struct {
		perSec float64
		want   int
	}{{41, 2}, {80, 2}, {81, 5}, {200, 5}, {201, 10}, {5_000, 200}} {
		if got := oneIn(c.perSec); got != c.want {
			t.Errorf("oneIn(%v) = %d, want %d", c.perSec, got, c.want)
		}
	}
}

// Sampling holds until the rate is well under the threshold, so a rate
// hovering at it doesn't flap the header, and then says it's over.
func TestSamplerStopsBelowTheExitRate(t *testing.T) {
	t0 := time.Unix(1_000, 0)
	s := newSampler(t0)
	sampled(s, "a", 250) // 50/s
	if n, _ := s.roll(t0.Add(sampleWindow)); n.OneIn != 2 {
		t.Fatalf("at 50/s: 1 in %d, want 2", n.OneIn)
	}
	sampled(s, "b", 175) // 35/s: under 40, over the exit rate
	if n, _ := s.roll(t0.Add(2 * sampleWindow)); n.OneIn != 2 {
		t.Fatalf("at 35/s while sampling: 1 in %d, want still 2", n.OneIn)
	}
	sampled(s, "c", 100) // 20/s
	n, changed := s.roll(t0.Add(3 * sampleWindow))
	if !changed || n.OneIn != 1 {
		t.Fatalf("at 20/s: %+v changed %v, want 1 in 1, changed", n, changed)
	}
}

// A streamed request arrives in flight and settles later. A row the stream
// sent always gets its settle, even if the rate changed in between, and the
// rate counts requests (settles), not both events.
func TestSamplerSettlesEveryRowItSent(t *testing.T) {
	t0 := time.Unix(1_000, 0)
	s := newSampler(t0)
	var sent []string
	for i := range 200 {
		r := model.Receipt{ID: fmt.Sprintf("f-%d", i), InFlight: true}
		if s.admit(r) {
			sent = append(sent, r.ID)
		}
	}
	if len(sent) != 200 {
		t.Fatalf("sent %d of 200 in-flight rows before sampling", len(sent))
	}
	if n, _ := s.roll(t0.Add(sampleWindow)); n.OneIn != 1 || n.RatePerSec != 0 {
		t.Fatalf("in-flight rows counted as requests: %+v", n)
	}
	sampled(s, "x", 4_000)
	if n, _ := s.roll(t0.Add(2 * sampleWindow)); n.OneIn != 20 {
		t.Fatalf("1 in %d, want 20", n.OneIn)
	}
	for _, id := range sent {
		if !s.admit(model.Receipt{ID: id}) {
			t.Fatalf("settle of %s, which the stream sent in flight, was sampled out", id)
		}
	}
}

// Each connection samples on its own rate: a filter narrow enough sees
// everything it matches ("Add a filter to see everything matching").
func TestHubSamplesPerSubscriber(t *testing.T) {
	h := NewHub()
	busy := h.subscribe(filter{tenant: "demo"})
	narrow := h.subscribe(filter{tenant: "demo", q: store.ReceiptQuery{Verdicts: []string{"blocked"}}})
	busy.sample.n = 20
	for i := range 200 {
		h.publish(model.Receipt{ID: fmt.Sprintf("r-%d", i), TenantID: "demo", Verdict: "allowed"})
	}
	for i := range 10 {
		h.publish(model.Receipt{ID: fmt.Sprintf("b-%d", i), TenantID: "demo", Verdict: "blocked"})
	}
	if got := len(busy.ch); got == 0 || got > 40 {
		t.Fatalf("the sampled stream got %d of 210", got)
	}
	if got := len(narrow.ch); got != 10 {
		t.Fatalf("the filtered stream got %d of its 10", got)
	}
	if busy.dropped.Load() != 0 {
		t.Fatal("sampled-out receipts were counted as dropped")
	}
}
