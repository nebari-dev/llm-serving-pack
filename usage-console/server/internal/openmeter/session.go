package openmeter

import (
	"net/http"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/buildinfo"
)

// session: everyone who reaches the console (the gateway signs them in) is a
// viewer, and the console has nothing to write.
func (s *Server) session(*http.Request) (any, error) {
	var out Session
	out.Tenant = SessionTenant{ID: s.Tenant, Name: s.Tenant}
	out.Environment = "openmeter"
	out.Actor = SessionActor{Email: "viewer", Roles: []string{"viewer"}}
	out.Auth = SessionAuth{Mode: "dev"}
	out.Versions.ControlPlane = buildinfo.Get()
	return out, nil
}
