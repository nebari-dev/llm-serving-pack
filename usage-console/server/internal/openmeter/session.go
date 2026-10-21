package openmeter

import (
	"net/http"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/api"
	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/buildinfo"
)

// writes are the console's write actions; this API has none of them.
var writes = []api.Action{
	api.ActDraftRules, api.ActPublishRules, api.ActKillSwitch, api.ActCapture, api.ActPrices, api.ActBudgets,
	api.ActRouting, api.ActProjects, api.ActDetectors, api.ActOwnKeys, api.ActAnyKeys, api.ActMembers,
}

// session: everyone who reaches the console (the gateway signs them in) is a
// viewer. Reading is allowed; writes don't exist here, so no role may.
func (s *Server) session(*http.Request) (any, error) {
	var out api.Session
	out.Tenant = api.SessionTenant{ID: s.Tenant, Name: s.Tenant}
	out.Environment = "openmeter"
	out.Actor = api.SessionActor{Email: "viewer", Roles: []string{"viewer"}, Role: "viewer", Authenticated: true}
	out.Auth = api.SessionAuth{Mode: "dev"}
	out.Permissions = map[api.Action]api.Permission{api.ActRead: {Allowed: true, Roles: []string{"viewer"}}}
	for _, a := range writes {
		out.Permissions[a] = api.Permission{Allowed: false, Roles: []string{}}
	}
	out.Versions.ControlPlane = buildinfo.Get()
	return out, nil
}
