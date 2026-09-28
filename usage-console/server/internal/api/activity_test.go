package api

import (
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/store"
)

var t0 = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// buckets makes one 5-minute tenant bucket per entry, starting at from.
func buckets(from time.Time, backend string, rs ...store.ActivityBucket) []store.ActivityBucket {
	for i := range rs {
		rs[i].Start, rs[i].Backend = from.Add(time.Duration(i)*5*time.Minute), backend
	}
	return rs
}

func flat(n int, b store.ActivityBucket) []store.ActivityBucket {
	out := make([]store.ActivityBucket, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestImpactComparesAnHourEitherSide(t *testing.T) {
	at := t0.Add(2 * time.Minute) // mid-bucket: the change's own bucket counts as after
	before := flat(12, store.ActivityBucket{Requests: 10, Served: 10, CostUSD: 0.2, Errors: 0, BlockedRedacted: 1})
	after := flat(12, store.ActivityBucket{Requests: 10, Served: 8, CostUSD: 0.4, Errors: 2, BlockedRedacted: 1})
	bs := buckets(t0.Add(-time.Hour), "a", append(before, after...)...)
	// Another backend's rows in the same buckets add to the tenant total.
	bs = append(bs, buckets(t0.Add(-time.Hour), "b", flat(24, store.ActivityBucket{Requests: 0})...)...)

	im := impactAt(bs, at, t0.Add(3*time.Hour))
	if im.WindowMinutes != 60 || im.Before.Requests != 120 || im.After.Requests != 120 {
		t.Fatalf("window %d, before %d, after %d", im.WindowMinutes, im.Before.Requests, im.After.Requests)
	}
	if len(im.Bins) != 24 || im.Split != 12 || im.Bins[0] != 10 {
		t.Fatalf("bins %v split %d", im.Bins, im.Split)
	}
	// Cost per served request: $0.02 before, $0.05 after.
	if !near(im.Before.CostPerRequestUSD, 0.02) || !near(im.After.CostPerRequestUSD, 0.05) {
		t.Fatalf("cost/request %v → %v", im.Before.CostPerRequestUSD, im.After.CostPerRequestUSD)
	}
	if !near(im.Before.ErrorRate, 0) || !near(im.After.ErrorRate, 0.2) || !near(im.After.BlockedRedactedShare, 0.1) {
		t.Fatalf("rates %+v %+v", im.Before, im.After)
	}
	// Error rate went from 0, so only cost/request moved (+150%), and it got worse.
	if !im.Comparable || im.EffectTone != "bad" || im.Effect != "cost/request +150%" {
		t.Fatalf("effect %q %q comparable %v", im.EffectTone, im.Effect, im.Comparable)
	}
}

func TestImpactWindowStopsAtNow(t *testing.T) {
	bs := buckets(t0.Add(-time.Hour), "a", flat(16, store.ActivityBucket{Requests: 30, Served: 30, CostUSD: 3})...)
	// 17 minutes after the change's bucket: three complete buckets after. The
	// one still filling would read as a drop in requests, so it's left out.
	im := impactAt(bs, t0, t0.Add(17*time.Minute))
	if im.WindowMinutes != 15 || len(im.Bins) != 6 || im.Split != 3 || im.Before.Requests != 90 || im.After.Requests != 90 {
		t.Fatalf("window %d bins %v split %d before %d after %d", im.WindowMinutes, im.Bins, im.Split, im.Before.Requests, im.After.Requests)
	}
	if im.EffectTone != "neutral" || im.Effect != "Nothing moved by 5% or more." {
		t.Fatalf("effect %q %q", im.EffectTone, im.Effect)
	}
	// Inside its first bucket, a change has nothing complete to compare yet.
	if im := impactAt(bs, t0, t0.Add(2*time.Minute)); im.WindowMinutes != 0 || im.Comparable || len(im.Bins) != 0 {
		t.Fatalf("first bucket: %+v", im)
	}
}

func TestEffectRules(t *testing.T) {
	base := Agg{Requests: 100, CostPerRequestUSD: 0.02, ErrorRate: 0.02, BlockedRedactedShare: 0.1}
	better := base
	better.CostPerRequestUSD, better.ErrorRate = 0.01, 0.01
	if tone, text, ok := effectOf(base, better); !ok || tone != "good" || text != "cost/request −50%, error rate −50%" {
		t.Fatalf("improved: %q %q %v", tone, text, ok)
	}
	mixed := base
	mixed.CostPerRequestUSD, mixed.BlockedRedactedShare = 0.01, 0.2
	if tone, _, _ := effectOf(base, mixed); tone != "bad" {
		t.Fatalf("any metric worse is a regression: %q", tone)
	}
	few := base
	few.Requests = 19
	tone, text, ok := effectOf(base, few)
	if ok || tone != "neutral" || !strings.Contains(text, "Too little traffic") || !strings.Contains(text, "100 before, 19 after") {
		t.Fatalf("thin traffic: %q %q %v", tone, text, ok)
	}
}

func TestBackendEventsFollowTheBannerThreshold(t *testing.T) {
	ok := store.ActivityBucket{Requests: 10}
	bad := store.ActivityBucket{Requests: 10, Errors: 3}
	seq := []store.ActivityBucket{ok, ok, ok, bad, ok, ok, ok, ok, ok}
	// "—" is requests refused before routing: never a failing backend.
	bs := append(buckets(t0, "vllm-internal", seq...), buckets(t0, "—", flat(9, store.ActivityBucket{Requests: 10, Errors: 10})...)...)

	evs := backendEvents(bs, t0)
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	// 3 of 30 in the 15 minutes ending with bucket 3 is 10%: failing from its start.
	start, end := evs[0], evs[1]
	if start.Kind != "backend_failing" || start.TS != t0.Add(15*time.Minute).UnixMilli() || start.Tone != "degraded" || !strings.Contains(start.Title, "vllm-internal") {
		t.Fatalf("start %+v", start)
	}
	if !strings.Contains(start.Detail, "10% of its 30 requests") || !strings.Contains(start.To, "backend=vllm-internal") {
		t.Fatalf("start detail %+v", start)
	}
	// Bucket 3 leaves the window with bucket 6.
	if end.Kind != "backend_recovered" || end.TS != t0.Add(30*time.Minute).UnixMilli() || end.Tone != "allowed" {
		t.Fatalf("end %+v", end)
	}

	// Under 20 requests in the window isn't enough to call it failing.
	thin := buckets(t0, "a", store.ActivityBucket{Requests: 5, Errors: 5}, store.ActivityBucket{Requests: 5, Errors: 5})
	if evs := backendEvents(thin, t0); len(evs) != 0 {
		t.Fatalf("thin traffic flagged: %+v", evs)
	}
	// Transitions before the range are context, not events.
	if evs := backendEvents(buckets(t0, "vllm-internal", seq...), t0.Add(20*time.Minute)); len(evs) != 1 || evs[0].Kind != "backend_recovered" {
		t.Fatalf("since filter: %+v", evs)
	}
}

func TestSpendCrossings(t *testing.T) {
	pts := []spendPoint{{t0, 50}, {t0.Add(time.Hour), 30}, {t0.Add(2 * time.Hour), 20}, {t0.Add(3 * time.Hour), 5}}
	// $10 already spent this period; cap $100.
	cs := spendCrossings(10, pts, 100, []float64{0.8, 1})
	if len(cs) != 2 || cs[0].Level != 0.8 || !cs[0].At.Equal(t0.Add(time.Hour)) || cs[1].Level != 1 || !cs[1].At.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("crossings %+v", cs)
	}
	if !near(cs[0].Before, 60) || !near(cs[1].Before, 90) {
		t.Fatalf("spend before each crossing bucket %+v", cs)
	}
	// A level reached before the points start isn't crossed in them.
	if cs := spendCrossings(85, pts, 100, []float64{0.8, 1}); len(cs) != 1 || cs[0].Level != 1 || !cs[0].At.Equal(t0) {
		t.Fatalf("already past 80%%: %+v", cs)
	}
	if cs := spendCrossings(0, pts, 0, []float64{0.8, 1}); len(cs) != 0 {
		t.Fatalf("no cap: %+v", cs)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
