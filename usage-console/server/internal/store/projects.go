package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

// Projects lists the tenant's projects (§5.2), by team then name, deleted
// ones included (the API leaves them out; Spend still names their history).
func (s *Store) Projects(ctx context.Context, tenant string) ([]model.Project, error) {
	rows, _ := s.Config.Query(ctx, `SELECT id, team_id, name, deleted_at IS NOT NULL FROM projects WHERE tenant_id = $1 ORDER BY team_id, lower(name), id`, tenant)
	return collect(rows, func(r pgx.Rows) (model.Project, error) {
		var p model.Project
		err := r.Scan(&p.ID, &p.Team, &p.Name, &p.Deleted)
		p.ETag = ProjectETag(p)
		return p, err
	})
}

// ProjectNameMax is how long a project's name may be, in characters.
const ProjectNameMax = 80

// ValidateProjectName checks a project's name, which is for people:
// receipts, budgets and rules carry the project's id. Any letters, spaces
// and punctuation go, trimmed; control characters don't.
func ValidateProjectName(name string) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(name) > ProjectNameMax:
		return fmt.Errorf("name is at most %d characters", ProjectNameMax)
	case strings.ContainsFunc(name, unicode.IsControl):
		return errors.New("name can't contain control characters")
	}
	return nil
}

// ProjectETag is a project's version for If-Match: its name is all a write
// can change.
func ProjectETag(p model.Project) string { return ETag([]string{p.ID, p.Name}) }

// KeyProject is the project a new key goes in: one of its team's live
// projects, by id or else by name. A key no longer creates its project, so a
// typo can't make a stray one.
func KeyProject(projects []model.Project, team, id, name string) (model.Project, error) {
	name = strings.TrimSpace(name)
	if id == "" && name == "" {
		return model.Project{}, errors.New("project is required")
	}
	i := slices.IndexFunc(projects, func(p model.Project) bool {
		return !p.Deleted && p.Team == team && (id != "" && p.ID == id || id == "" && strings.EqualFold(p.Name, name))
	})
	switch {
	case i >= 0:
		return projects[i], nil
	case id != "":
		return model.Project{}, fmt.Errorf("Project %s isn't one of team %s's projects.", id, team)
	}
	return model.Project{}, fmt.Errorf("Team %s has no project named %q. Create the project first, then the key.", team, name)
}

// ProjectDeleteRefusal says why a project can't be deleted yet, or nil. An
// active key in it would be left without a project, and a budget on it
// would cap nothing. Revoked keys don't count: their history stays under
// the deleted project's name.
func ProjectDeleteRefusal(p model.Project, keys []model.APIKey, budgets []model.Budget) error {
	var active []string
	for _, k := range keys {
		if k.ProjectID == p.ID && k.Status != "revoked" {
			active = append(active, k.Name)
		}
	}
	slices.Sort(active)
	budgeted := slices.ContainsFunc(budgets, func(b model.Budget) bool { return b.ScopeType == "project" && b.Scope == p.ID })
	var has, todo []string
	switch len(active) {
	case 0:
	case 1:
		has, todo = append(has, "1 active key ("+active[0]+")"), append(todo, "revoke the key")
	default:
		has, todo = append(has, fmt.Sprintf("%d active keys (%s)", len(active), strings.Join(active, ", "))), append(todo, "revoke the keys")
	}
	if budgeted {
		has, todo = append(has, "a budget"), append(todo, "delete the budget on Spend")
	}
	if len(has) == 0 {
		return nil
	}
	do := strings.Join(todo, " and ")
	return fmt.Errorf("Project %s still has %s. %s first.", p.Name, strings.Join(has, " and "), strings.ToUpper(do[:1])+do[1:])
}

// CreateProject adds a project to a team. A name the team already uses
// (ignoring case) is ErrConflict; an unknown team is a foreign-key error for
// the caller.
func (s *Store) CreateProject(ctx context.Context, tenant, actor, team, name string) (model.Project, error) {
	name = strings.TrimSpace(name)
	p := model.Project{Team: team, Name: name, ID: demo.ProjectID(tenant, team, name)}
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	// Ids derive from tenant, team and name, as migration 011 made them for
	// projects that predate the table; a renamed or deleted project may
	// hold that id already, so a new one then gets a random id.
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, p.ID).Scan(&taken); err != nil {
		return p, err
	}
	if taken {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		p.ID = "p" + hex.EncodeToString(b)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, tenant_id, team_id, name) VALUES ($1,$2,$3,$4)`, p.ID, tenant, team, name); err != nil {
		return p, uniqueConflict(err)
	}
	if err := audit(ctx, tx, tenant, actor, "Created project", team+" / "+name, "Project", p.ID, nil, p); err != nil {
		return p, err
	}
	p.ETag = ProjectETag(p)
	return p, tx.Commit(ctx)
}

// project reads a live project for update.
func project(ctx context.Context, tx pgx.Tx, tenant, id string) (model.Project, error) {
	p := model.Project{ID: id}
	err := tx.QueryRow(ctx, `SELECT team_id, name FROM projects WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL FOR UPDATE`, tenant, id).Scan(&p.Team, &p.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	p.ETag = ProjectETag(p)
	return p, err
}

// RenameProject changes a project's name. Its keys, budgets, rules and
// receipts name it by id, so nothing else changes; receipts already written
// keep the name they were made under. A name the team uses is ErrConflict.
func (s *Store) RenameProject(ctx context.Context, tenant, actor, id, ifMatch, name string) (model.Project, error) {
	name = strings.TrimSpace(name)
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return model.Project{}, err
	}
	defer tx.Rollback(ctx)
	was, err := project(ctx, tx, tenant, id)
	if err != nil {
		return was, err
	}
	if err := checkMatch(ifMatch, was); err != nil {
		return was, err
	}
	if name == was.Name {
		return was, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET name = $3 WHERE tenant_id = $1 AND id = $2`, tenant, id, name); err != nil {
		return was, uniqueConflict(err)
	}
	now := was
	now.Name = name
	now.ETag = ProjectETag(now)
	if err := audit(ctx, tx, tenant, actor, "Renamed project", fmt.Sprintf("%s / %s → %s", was.Team, was.Name, name), "Project", id, was, now); err != nil {
		return was, err
	}
	return now, tx.Commit(ctx)
}

// DeleteProject marks a project deleted, if ProjectDeleteRefusal allows:
// refusal is returned as is, for a 409. Its name is free again.
func (s *Store) DeleteProject(ctx context.Context, tenant, actor, id, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	was, err := project(ctx, tx, tenant, id)
	if err != nil {
		return err
	}
	if err := checkMatch(ifMatch, was); err != nil {
		return err
	}
	// Keys and budgets are read in the same transaction; the project row is
	// locked, so a key can't join it meanwhile (key creation reads it FOR SHARE).
	rows, _ := tx.Query(ctx, `SELECT name, status FROM api_keys WHERE tenant_id = $1 AND project_id = $2`, tenant, id)
	keys, err := collect(rows, func(r pgx.Rows) (model.APIKey, error) {
		k := model.APIKey{ProjectID: id}
		return k, r.Scan(&k.Name, &k.Status)
	})
	if err != nil {
		return err
	}
	var budgeted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM budgets WHERE tenant_id = $1 AND scope_type = 'project' AND scope = $2)`, tenant, id).Scan(&budgeted); err != nil {
		return err
	}
	var budgets []model.Budget
	if budgeted {
		budgets = []model.Budget{{ScopeType: "project", Scope: id}}
	}
	if err := ProjectDeleteRefusal(was, keys, budgets); err != nil {
		return &InUseError{err.Error()}
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, tenant, id); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted project", was.Team+" / "+was.Name, "Project", id, was, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InUseError is a delete refused because something still depends on the
// resource; the message says what, and what to do.
type InUseError struct{ Reason string }

func (e *InUseError) Error() string { return e.Reason }
