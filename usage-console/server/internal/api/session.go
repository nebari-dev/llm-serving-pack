package api

import (
	"net/http"
	"strings"

	"github.com/jbouder/stargate/server/internal/buildinfo"
)

// Session is what the console's shell shows about where it is and who's
// using it: tenant, environment, the caller, and the versions in play.
type Session struct {
	Tenant      SessionTenant `json:"tenant"`
	Environment string        `json:"environment"`
	Actor       SessionActor  `json:"actor"`
	// Auth is how people sign in here (dev mode or OIDC), and where.
	Auth SessionAuth `json:"auth"`
	// Permissions is, per action in the role table, whether the caller may
	// do it and which roles may: the console disables what they can't, with
	// "Needs role …".
	Permissions map[Action]Permission `json:"permissions"`
	Versions    struct {
		ControlPlane string `json:"controlPlane"`
	} `json:"versions"`
	// Warden is nil when this control plane doesn't know where Warden is
	// (serve without -warden, e.g. make dev on the dev gateway).
	Warden *SessionWarden `json:"warden"`
	// GatewayURL is the OpenAI-compatible base callers point at, "" if unknown.
	GatewayURL string `json:"gatewayUrl"`
}

type SessionTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionActor struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	// Roles are from the token's Keycloak groups, most powerful first; Role
	// is the first ("" with none).
	Roles []string `json:"roles"`
	Role  string   `json:"role,omitempty"`
	// Authenticated is false in dev mode, where every caller is the dev user
	// without signing in.
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
	u, _ := userFrom(r.Context())
	out.Actor, out.Auth, out.Permissions = s.sessionAuth(u)
	out.Versions.ControlPlane = buildinfo.Get()
	if s.GatewayURL != "" {
		out.GatewayURL = strings.TrimSuffix(s.GatewayURL, "/") + "/v1"
	}
	if s.WardenURL != "" {
		out.Warden = &SessionWarden{}
		if h, err := s.fetchWarden(r.Context()); err == nil {
			*out.Warden = SessionWarden{Connected: true, Version: h.Version, SnapshotAgeSeconds: h.SnapshotAgeSeconds, Passthrough: h.Passthrough}
		}
	}
	return out, nil
}
