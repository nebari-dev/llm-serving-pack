package store

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
)

// The real seed is what a fresh dev stack starts with: nothing simulated.
// Only the backend that's actually there (the local model server), its
// route, the models it serves, and team names to put keys in.
func TestRealSeedHasNothingSimulated(t *testing.T) {
	s, err := seedFor(SeedReal)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Backends) != 1 || s.Backends[0].Name != "local" {
		t.Fatalf("backends = %v", s.Backends)
	}
	for _, b := range s.Backends {
		if b.Endpoint == nil || strings.Contains(b.Endpoint.Port, "8090") {
			t.Errorf("backend %s points at the fake upstream or nowhere: %+v", b.Name, b.Endpoint)
		}
	}
	if len(s.Routes) != 1 || s.Routes[0].Targets[0].Backend != "local" {
		t.Errorf("routes = %v", s.Routes)
	}
	if len(s.Models) != 1 || s.Models[0].ID != "smollm2" {
		t.Errorf("models = %v", s.Models)
	}
	if len(s.Teams) != len(demo.Teams) {
		t.Errorf("teams = %v", s.Teams)
	}
	if n := len(s.Keys) + len(s.Budgets) + len(s.Projects) + len(s.Policies) + len(s.Aliases) + len(s.Prices) + len(s.LiteLLMKeys) + len(s.Detectors); n != 0 || s.Changes {
		t.Errorf("simulated rows in the real seed: %+v", s)
	}
	if s.TenantName == "Demo tenant" {
		t.Error("the real seed isn't a demo")
	}
}

func TestDemoSeedIsTheWholeDemo(t *testing.T) {
	s, err := seedFor(SeedDemo)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Backends) != len(demo.Backends) || len(s.Keys) != len(demo.Keys) || !s.Changes || s.TenantName != "Demo tenant" {
		t.Errorf("demo seed = %+v", s)
	}
	if _, err := seedFor("fake"); err == nil {
		t.Error("an unknown seed is refused")
	}
}
