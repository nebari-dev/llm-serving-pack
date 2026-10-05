package api

import (
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Health, p50 and error rate come from receipts, never from the seed: the
// last 15 minutes (the banner's window) say healthy, degraded or down, and a
// backend with no recent traffic is idle, not assumed healthy.
func TestBackendHealthIsObserved(t *testing.T) {
	seeded := model.Backend{Name: "b", Health: "healthy", P50: 412, ErrorRate: 3.1}
	cases := []struct {
		name   string
		recent *store.BackendFailures
		hour   *store.BackendStats
		health string
		p50    int
		errs   float64
		n      int
	}{
		{"no traffic", nil, nil, "idle", 0, 0, 0},
		{"quiet now, traffic earlier", nil, &store.BackendStats{P50: 300, ErrorRate: 1.25, Requests: 40}, "idle", 300, 1.3, 40},
		{"serving", &store.BackendFailures{Total: 50, Failed: 1}, &store.BackendStats{P50: 220, ErrorRate: 2, Requests: 50}, "healthy", 220, 2, 50},
		{"failing some", &store.BackendFailures{Total: 40, Failed: 6}, &store.BackendStats{P50: 900, ErrorRate: 15, Requests: 40}, "degraded", 900, 15, 40},
		{"failing all", &store.BackendFailures{Total: 3, Failed: 3}, &store.BackendStats{P50: 80, ErrorRate: 100, Requests: 3}, "down", 80, 100, 3},
		{"one stray failure among successes", &store.BackendFailures{Total: 10, Failed: 1}, &store.BackendStats{P50: 80, ErrorRate: 10, Requests: 10}, "healthy", 80, 10, 10},
		// Too few to call it down, but nothing it served succeeded.
		{"every one of two failing", &store.BackendFailures{Total: 2, Failed: 2}, &store.BackendStats{P50: 80, ErrorRate: 100, Requests: 2}, "degraded", 80, 100, 2},
	}
	for _, c := range cases {
		b := observedBackend(seeded, c.recent, c.hour)
		if b.Health != c.health || b.P50 != c.p50 || b.ErrorRate != c.errs || b.Requests1h != c.n {
			t.Errorf("%s: got %s p50=%d err=%v n=%d, want %s p50=%d err=%v n=%d", c.name, b.Health, b.P50, b.ErrorRate, b.Requests1h, c.health, c.p50, c.errs, c.n)
		}
	}
}
