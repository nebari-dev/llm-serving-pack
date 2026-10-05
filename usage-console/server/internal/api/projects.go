package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// projects lists the tenant's projects (§5.2), keys or not.
func (s *Server) projects(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.Projects(r.Context(), t)
}

// createProject takes {team, name}. Projects can't be renamed or deleted
// yet; a name the team already has is a 409.
func (s *Server) createProject(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in model.Project
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Team == "" {
		return nil, badRequest("team is required")
	}
	if err := store.ValidateProjectName(in.Name); err != nil {
		return nil, badRequest(err.Error())
	}
	p, err := s.Store.CreateProject(r.Context(), t, s.DevActor, in.Team, in.Name)
	if errors.Is(err, store.ErrConflict) {
		return nil, conflict("team " + in.Team + " already has a project named " + in.Name)
	}
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23503" {
		return nil, badRequest("unknown team")
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return p, nil
}

// checkKeyProject validates a new key's project: an existing project of the
// key's team, or a valid name for one created with the key.
func (s *Server) checkKeyProject(ctx context.Context, t, team, name string) error {
	ps, err := s.Store.Projects(ctx, t)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(ps, func(p model.Project) bool { return p.Team == team && p.Name == name }) {
		return nil
	}
	if err := store.ValidateProjectName(name); err != nil {
		return badRequest("project " + err.Error())
	}
	return nil
}
