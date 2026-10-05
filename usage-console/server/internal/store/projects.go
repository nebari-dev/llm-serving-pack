package store

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

// Projects lists the tenant's projects (§5.2), by team then name.
func (s *Store) Projects(ctx context.Context, tenant string) ([]model.Project, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, team_id, name FROM projects WHERE tenant_id = $1 ORDER BY team_id, name`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Project, error) {
		var p model.Project
		return p, r.Scan(&p.ID, &p.Team, &p.Name)
	})
}

var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidateProjectName checks a new project's name. Names travel in headers,
// receipts and rule conditions, so they're kept to a slug. Projects made
// from keys before the table existed keep whatever name they had.
func ValidateProjectName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case len(name) > 63:
		return errors.New("name is at most 63 characters")
	case !projectName.MatchString(name):
		return errors.New("name may use lowercase letters, digits, - and _, starting with a letter or digit")
	}
	return nil
}

// CreateProject adds a project to a team. A name the team already uses is
// ErrConflict; an unknown team is a foreign-key error for the caller.
func (s *Store) CreateProject(ctx context.Context, tenant, actor, team, name string) (model.Project, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return model.Project{}, err
	}
	defer tx.Rollback(ctx)
	p, created, err := ensureProject(ctx, tx, tenant, actor, team, name)
	if err != nil {
		return p, err
	}
	if !created {
		return p, ErrConflict
	}
	return p, tx.Commit(ctx)
}

// ensureProject finds the team's project by name, or creates it with an
// audit row. Ids derive from tenant, team and name (demo.ProjectID), as
// migration 011 made them for projects that predate the table.
func ensureProject(ctx context.Context, tx pgx.Tx, tenant, actor, team, name string) (model.Project, bool, error) {
	p := model.Project{Team: team, Name: name}
	err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE tenant_id = $1 AND team_id = $2 AND name = $3`, tenant, team, name).Scan(&p.ID)
	if err == nil {
		return p, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, false, err
	}
	p.ID = demo.ProjectID(tenant, team, name)
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, tenant_id, team_id, name) VALUES ($1,$2,$3,$4)`, p.ID, tenant, team, name); err != nil {
		return p, false, uniqueConflict(err)
	}
	if err := audit(ctx, tx, tenant, actor, "Created project", team+" / "+name, "Project", p.ID, nil, p); err != nil {
		return p, false, err
	}
	return p, true, nil
}
