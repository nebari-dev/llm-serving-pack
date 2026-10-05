package demo

import "testing"

// The seed inserts Projects and then Keys pointing at them, so every key's
// project must be one of them, on the key's team.
func TestKeysReferenceSeededProjects(t *testing.T) {
	byID := map[string]Project{}
	for _, p := range Projects {
		byID[p.ID] = p
	}
	for _, k := range Keys {
		p, ok := byID[k.ProjectID]
		if !ok || p.Team != k.Team || p.Name != k.Project {
			t.Errorf("%s: project %q (%s/%s) not seeded as such: %+v", k.ID, k.ProjectID, k.Team, k.Project, p)
		}
	}
	if len(Projects) != 8 {
		t.Errorf("%d projects, want one per seeded key's project: %+v", len(Projects), Projects)
	}
}

// Config migration 011 derives the same ids in SQL for projects it creates
// from existing keys.
func TestProjectIDIsStable(t *testing.T) {
	if got := ProjectID("demo", "support", "helpdesk"); got != "p"+HashSecret("demo/support/helpdesk")[:8] || len(got) != 9 {
		t.Errorf("ProjectID = %q", got)
	}
	if ProjectID("demo", "web", "helpdesk") == ProjectID("demo", "support", "helpdesk") {
		t.Error("same name on two teams is two projects")
	}
}
