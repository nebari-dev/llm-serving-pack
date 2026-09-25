package api

import (
	"net/http"

	"github.com/jbouder/stargate/server/internal/buildinfo"
)

// Session is what the console's shell shows about where it is and who's
// using it: tenant, environment, the caller, and the versions in play.
type Session struct {
	Tenant      SessionTenant `json:"tenant"`
	Environment string        `json:"environment"`
	Actor       SessionActor  `json:"actor"`
	Versions    struct {
		ControlPlane string `json:"controlPlane"`
	} `json:"versions"`
	// Warden is nil when this control plane doesn't know where Warden is
	// (serve without -warden, e.g. make dev on the dev gateway).
	Warden *SessionWarden `json:"warden"`
}

type SessionTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionActor struct {
	Email string `json:"email"`
	// Authenticated is false until OIDC is wired (§6); every caller is the
	// dev actor.
	Authenticated bool `json:"authenticated"`
}

type SessionWarden struct {
	Connected          bool    `json:"connected"`
	Version            string  `json:"version,omitempty"`
	SnapshotAgeSeconds float64 `json:"snapshotAgeSeconds,omitempty"`
	Passthrough        bool    `json:"passthrough"`
}

func (s *Server) session(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	name, err := s.Store.TenantName(r.Context(), t)
	if err != nil {
		return nil, err
	}
	var out Session
	out.Tenant.ID, out.Tenant.Name = t, name
	out.Environment = s.Environment
	out.Actor = SessionActor{Email: s.DevActor}
	out.Versions.ControlPlane = buildinfo.Get()
	if s.WardenURL != "" {
		out.Warden = &SessionWarden{}
		if h, err := s.fetchWarden(r.Context()); err == nil {
			*out.Warden = SessionWarden{Connected: true, Version: h.Version, SnapshotAgeSeconds: h.SnapshotAgeSeconds, Passthrough: h.Passthrough}
		}
	}
	return out, nil
}
