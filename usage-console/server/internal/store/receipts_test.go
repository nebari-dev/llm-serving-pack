package store

import (
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
)

// Throttled requests are their own verdict in the traffic series, not blocks.
func TestSeriesPointCountsEachVerdict(t *testing.T) {
	var p model.SeriesPoint
	for v, n := range map[string]int{"allowed": 1, "redacted": 2, "rerouted": 3, "blocked": 4, "truncated": 5, "throttled": 6, "mystery": 7} {
		p.Add(v, n)
	}
	if want := (model.SeriesPoint{Allowed: 1, Redacted: 2, Rerouted: 3, Blocked: 4, Truncated: 5, Throttled: 6}); p != want {
		t.Fatalf("got %+v, want %+v", p, want)
	}
}

// The project backfill walks whole UTC days, newest first, so each update
// touches one daily chunk.
func TestBackfillDays(t *testing.T) {
	now := time.Date(2026, 10, 5, 13, 30, 0, 0, time.UTC)
	got := backfillDays(time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC), now)
	want := []time.Time{
		time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("day %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if len(backfillDays(time.Time{}, now)) != 0 {
		t.Fatal("no receipts, no days")
	}
}
