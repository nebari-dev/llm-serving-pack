package api

import (
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
