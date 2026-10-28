package openmeter

import (
	"net/http"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/buildinfo"
)

// session: the caller, a viewer (the console has nothing to write).
func (s *Server) session(r *http.Request) (any, error) {
	var out Session
	out.Tenant = SessionTenant{ID: s.Tenant, Name: s.Tenant}
	out.Environment = "openmeter"
	out.Actor, out.Auth = caller(r)
	out.Versions.ControlPlane = buildinfo.Get()
	return out, nil
}
