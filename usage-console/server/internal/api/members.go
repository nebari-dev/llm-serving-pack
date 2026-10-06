package api

import "net/http"

// members is GET /members: who has used the console and the roles their
// last sign-in's Keycloak groups gave them. Roles are assigned in Keycloak,
// not here.
func (s *Server) members(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Members(r.Context(), t)
}
