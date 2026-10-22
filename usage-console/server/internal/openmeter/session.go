package openmeter

import (
	"net/http"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/buildinfo"
)

// writes are the console's write actions; this API has none of them.
var writes = []Action{
	ActDraftRules, ActPublishRules, ActKillSwitch, ActCapture, ActPrices, ActBudgets,
	ActRouting, ActProjects, ActDetectors, ActOwnKeys, ActAnyKeys, ActMembers,
}

// session: everyone who reaches the console (the gateway signs them in) is a
// viewer. Reading is allowed; writes don't exist here, so no role may.
func (s *Server) session(*http.Request) (any, error) {
	var out Session
	out.Tenant = SessionTenant{ID: s.Tenant, Name: s.Tenant}
	out.Environment = "openmeter"
	out.Actor = SessionActor{Email: "viewer", Roles: []string{"viewer"}, Role: "viewer", Authenticated: true}
	out.Auth = SessionAuth{Mode: "dev"}
	out.Permissions = map[Action]Permission{ActRead: {Allowed: true, Roles: []string{"viewer"}}}
	for _, a := range writes {
		out.Permissions[a] = Permission{Allowed: false, Roles: []string{}}
	}
	out.Versions.ControlPlane = buildinfo.Get()
	return out, nil
}
