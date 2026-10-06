package store

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

// Project names are for people (decided 2026-10-05): any letters, spaces
// and punctuation, trimmed, up to 80 characters. Receipts and rules carry
// the project's id, so a name needn't be a slug.
func TestValidateProjectName(t *testing.T) {
	for name, want := range map[string]string{
		"helpdesk":              "",
		"nightly-digest_2":      "",
		"Help desk":             "",
		"Équipe données · 2026": "",
		strings.Repeat("é", 80): "",
		"":                      "name is required",
		"   ":                   "name is required",
		strings.Repeat("a", 81): "name is at most 80 characters",
		"tab\there":             "name can't contain control characters",
		"line\nbreak":           "name can't contain control characters",
	} {
		got := ""
		if err := ValidateProjectName(name); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

var teamProjects = []model.Project{
	{ID: "p1", Team: "support", Name: "helpdesk"},
	{ID: "p2", Team: "web", Name: "helpdesk"},
	{ID: "p3", Team: "support", Name: "old", Deleted: true},
}

// A key's project must exist on its team (decided 2026-10-05): POST /keys no
// longer creates one. It's named by id, or by name within the team.
func TestKeyProject(t *testing.T) {
	for _, c := range []struct {
		name, team, id, project, want, err string
	}{
		{"by id", "support", "p1", "", "p1", ""},
		{"by name", "support", "", "helpdesk", "p1", ""},
		{"same name, other team", "web", "", "helpdesk", "p2", ""},
		{"name trimmed", "support", "", "  helpdesk ", "p1", ""},
		{"name ignores case, as uniqueness does", "support", "", "HelpDesk", "p1", ""},
		{"id wins over name", "support", "p1", "anything", "p1", ""},
		{"unknown name", "support", "", "launch", "", `Team support has no project named "launch". Create the project first, then the key.`},
		{"another team's id", "support", "p2", "", "", `Project p2 isn't one of team support's projects.`},
		{"deleted", "support", "p3", "", "", `Project p3 isn't one of team support's projects.`},
		{"deleted by name", "support", "", "old", "", `Team support has no project named "old". Create the project first, then the key.`},
		{"none", "support", "", "", "", "project is required"},
	} {
		p, err := KeyProject(teamProjects, c.team, c.id, c.project)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.err || p.ID != c.want {
			t.Errorf("%s: got %q %q, want %q %q", c.name, p.ID, got, c.want, c.err)
		}
	}
}

// A project can go only when nothing would be left pointing at it: no active
// key (revoked ones keep their history under it) and no budget.
func TestProjectDeleteRefusal(t *testing.T) {
	p := model.Project{ID: "p1", Team: "support", Name: "helpdesk"}
	keys := []model.APIKey{
		{ID: "k1", Name: "support-bot", ProjectID: "p1", Status: "active"},
		{ID: "k2", Name: "support-batch", ProjectID: "p1", Status: "rotating"},
		{ID: "k3", Name: "old-bot", ProjectID: "p1", Status: "revoked"},
		{ID: "k4", Name: "web-bot", ProjectID: "p2", Status: "active"},
	}
	budgets := []model.Budget{{ID: "b1", ScopeType: "project", Scope: "p1"}, {ID: "b2", ScopeType: "team", Scope: "p1"}}
	for _, c := range []struct {
		name    string
		keys    []model.APIKey
		budgets []model.Budget
		want    string
	}{
		{"keys and budget", keys, budgets, "Project helpdesk still has 2 active keys (support-batch, support-bot) and a budget. Revoke the keys and delete the budget on Spend first."},
		{"one key", keys[:1], nil, "Project helpdesk still has 1 active key (support-bot). Revoke the key first."},
		{"budget", keys[2:], budgets, "Project helpdesk still has a budget. Delete the budget on Spend first."},
		{"only revoked and other projects' keys", keys[2:], budgets[1:], ""},
	} {
		got := ""
		if err := ProjectDeleteRefusal(p, c.keys, c.budgets); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A project's version covers what a write can change: its name.
func TestProjectETagFollowsTheName(t *testing.T) {
	a := model.Project{ID: "p1", Team: "support", Name: "helpdesk"}
	b := a
	b.Name = "Help desk"
	if ProjectETag(a) == ProjectETag(b) || ProjectETag(a) != ProjectETag(a) {
		t.Error("etag should change with the name only")
	}
}
