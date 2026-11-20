package oidc

import "testing"

func TestParseGroupForm(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    GroupForm
		wantErr bool
	}{
		{name: "empty defaults to path", in: "", want: GroupFormPath},
		{name: "path", in: "path", want: GroupFormPath},
		{name: "name", in: "name", want: GroupFormName},
		{name: "surrounding space is ignored", in: " name ", want: GroupFormName},
		{name: "unknown form is an error", in: "fullpath", wantErr: true},
		{name: "case matters", in: "Path", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGroupForm(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseGroupForm(%q) = %q, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGroupForm(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseGroupForm(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGroupFormNormalize(t *testing.T) {
	tests := []struct {
		name string
		form GroupForm
		in   string
		want string
	}{
		{name: "path: bare name gains a leading slash", form: GroupFormPath, in: "llm", want: "/llm"},
		{name: "path: full path is unchanged", form: GroupFormPath, in: "/llm", want: "/llm"},
		{name: "path: nested path keeps its parent", form: GroupFormPath, in: "/org/team-a", want: "/org/team-a"},
		{name: "path: empty segments and space are dropped", form: GroupFormPath, in: " /org//team-a/ ", want: "/org/team-a"},
		{name: "path: a bare slash is empty", form: GroupFormPath, in: "/", want: ""},
		{name: "path: empty stays empty", form: GroupFormPath, in: "", want: ""},
		{name: "name: value is compared as written", form: GroupFormName, in: "ops", want: "ops"},
		{name: "name: a slash is not added", form: GroupFormName, in: "/llm", want: "/llm"},
		{name: "name: surrounding space is trimmed", form: GroupFormName, in: " ops ", want: "ops"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.form.Normalize(tt.in); got != tt.want {
				t.Errorf("%s.Normalize(%q) = %q, want %q", tt.form, tt.in, got, tt.want)
			}
		})
	}
}
