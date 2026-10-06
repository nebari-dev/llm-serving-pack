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

// projects lists the tenant's projects (§5.2), keys or not, with each one's
// etag for a rename or delete. Deleted projects aren't listed.
func (s *Server) projects(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ps, err := s.Store.Projects(r.Context(), t)
	return slices.DeleteFunc(ps, func(p model.Project) bool { return p.Deleted }), err
}

// createProject takes {team, name}. A name the team already has (ignoring
// case) is a 409.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request, t string) (any, error) {
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
		return nil, conflict("Team " + in.Team + " already has a project named " + in.Name + ".")
	}
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23503" {
		return nil, badRequest("unknown team")
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	w.Header().Set("ETag", p.ETag)
	return p, nil
}

// renameProject is PUT /projects/{id} {name}, with If-Match. Keys, budgets,
// rules and receipts name the project by id, so they follow the rename.
func (s *Server) renameProject(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := store.ValidateProjectName(in.Name); err != nil {
		return nil, badRequest(err.Error())
	}
	p, err := s.Store.RenameProject(r.Context(), t, s.DevActor, r.PathValue("id"), m, in.Name)
	if errors.Is(err, store.ErrConflict) {
		return nil, conflict("Team " + p.Team + " already has a project named " + in.Name + ".")
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	w.Header().Set("ETag", p.ETag)
	return p, nil
}

// deleteProject is DELETE /projects/{id}, with If-Match. It's refused (409,
// saying why) while the project has an active key or a budget.
func (s *Server) deleteProject(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	err = s.Store.DeleteProject(r.Context(), t, s.DevActor, id, m)
	if inUse := (*store.InUseError)(nil); errors.As(err, &inUse) {
		return nil, conflict(inUse.Reason)
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]string{"id": id}, nil
}

// keyProject resolves a new key's project, by id or by name within the key's
// team. A project the team doesn't have is a 400 saying to create it first.
func (s *Server) keyProject(ctx context.Context, t string, in *store.NewKey) error {
	ps, err := s.Store.Projects(ctx, t)
	if err != nil {
		return err
	}
	p, err := store.KeyProject(ps, in.Team, in.ProjectID, in.Project)
	if err != nil {
		return badRequest(err.Error())
	}
	in.ProjectID, in.Project = p.ID, p.Name
	return nil
}
