package api

import (
	"slices"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/routing"
)

// Sync is what the gateway runs against the desired state: the seed sync_state
// column is never passed on.
func TestSyncFromWhatTheGatewayRuns(t *testing.T) {
	running := routing.Compile(demo.Backends, demo.Routes)
	added := model.Route{Name: "new", Match: model.RouteMatch{Models: []string{"x"}}, Targets: []model.RouteTarget{{Backend: "openai-prod"}}}
	rs := append(slices.Clone(demo.Routes), added)

	// Before any failed apply, what isn't running is pending.
	got := withRouteSync(slices.Clone(rs), demo.Backends, running, nil)
	if got[0].Sync != "synced" || got[len(got)-1].Sync != "pending" {
		t.Errorf("no failed apply: seeded %q, new %q", got[0].Sync, got[len(got)-1].Sync)
	}
	// A failed apply marks only what it tried, at the version it tried.
	tried := added
	tried.ETag = "v1"
	failed := map[string]string{"route/new": "v1"}
	if got := withRouteSync([]model.Route{tried}, demo.Backends, running, failed); got[0].Sync != "failed" {
		t.Errorf("a route the failed apply tried: %q, want failed", got[0].Sync)
	}
	edited := tried
	edited.ETag = "v2"
	if got := withRouteSync([]model.Route{edited}, demo.Backends, running, failed); got[0].Sync != "pending" {
		t.Errorf("a route edited since the failed apply: %q, want pending", got[0].Sync)
	}
	later := added
	later.Name, later.ETag = "later", "v1"
	if got := withRouteSync([]model.Route{later}, demo.Backends, running, failed); got[0].Sync != "pending" {
		t.Errorf("a route made after the failed apply: %q, want pending", got[0].Sync)
	}

	bs := slices.Clone(demo.Backends)
	moved := *bs[0].Endpoint
	moved.Port = "9090"
	bs[0].Endpoint = &moved
	got = nil
	gotB := withBackendSync(bs, running, map[string]string{"backend/openai-prod": "an older version"})
	want := map[string]string{"openai-prod": "pending", "anthropic-prod": "synced", "azure-openai-eu": "no_endpoint"}
	for _, b := range gotB {
		if w, ok := want[b.Name]; ok && b.Sync != w {
			t.Errorf("%s sync %q, want %q", b.Name, b.Sync, w)
		}
	}
}

// What an apply tries: each route and backend the gateway doesn't run as it
// is, at its current version.
func TestAttempted(t *testing.T) {
	running := routing.Compile(demo.Backends, demo.Routes)
	rs := slices.Clone(demo.Routes)
	rs = append(rs, model.Route{Name: "new", ETag: "v1", Match: model.RouteMatch{Models: []string{"x"}}, Targets: []model.RouteTarget{{Backend: "openai-prod"}}})
	bs := slices.Clone(demo.Backends)
	moved := *bs[1].Endpoint
	moved.Port = "9090"
	bs[1].Endpoint = &moved
	got := attempted(running, rs, bs)
	if len(got) != 2 || got["route/new"] != "v1" || got["backend/anthropic-prod"] == "" {
		t.Errorf("attempted = %v, want route/new and backend/anthropic-prod", got)
	}
}
