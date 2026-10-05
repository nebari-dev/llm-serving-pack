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

	for _, tc := range []struct {
		name       string
		lastFailed bool
		want       string
	}{{"pending until applied", false, "pending"}, {"failed when the last apply failed", true, "failed"}} {
		got := withRouteSync(slices.Clone(rs), running, tc.lastFailed)
		if got[0].Sync != "synced" || got[len(got)-1].Sync != tc.want {
			t.Errorf("%s: seeded %q, new %q", tc.name, got[0].Sync, got[len(got)-1].Sync)
		}
	}

	bs := slices.Clone(demo.Backends)
	moved := *bs[0].Endpoint
	moved.Port = "9090"
	bs[0].Endpoint = &moved
	got := withBackendSync(bs, running, false)
	want := map[string]string{"openai-prod": "pending", "anthropic-prod": "synced", "azure-openai-eu": "no_endpoint"}
	for _, b := range got {
		if w, ok := want[b.Name]; ok && b.Sync != w {
			t.Errorf("%s sync %q, want %q", b.Name, b.Sync, w)
		}
	}
}
