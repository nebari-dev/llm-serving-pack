package v1alpha1

import (
	"strings"
	"testing"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// TestValidateGroupNames pins #232: the operator normalizes access groups to
// Keycloak paths, and Envoy Gateway rejects a claim rule with no values, so a
// group that is blank or only slashes must be refused at admission rather
// than leave a model's SecurityPolicy unappliable.
func TestValidateGroupNames(t *testing.T) {
	tests := []struct {
		name    string
		groups  []string
		wantErr string
	}{
		{name: "bare name", groups: []string{"llm"}},
		{name: "full path", groups: []string{"/llm"}},
		{name: "nested path", groups: []string{"/org/team-a"}},
		{name: "empty string", groups: []string{"llm", ""}, wantErr: "spec.access.groups[1]"},
		{name: "only spaces", groups: []string{"  "}, wantErr: "spec.access.groups[0]"},
		{name: "only slashes", groups: []string{"/"}, wantErr: "spec.access.groups[0]"},
		{name: "slashes and spaces", groups: []string{" // "}, wantErr: "spec.access.groups[0]"},
		{name: "newline, as the operator trims all space", groups: []string{"\n"}, wantErr: "spec.access.groups[0]"},
		{name: "a space between slashes is a name", groups: []string{"/ /"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGroupNames(tt.groups)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateGroupNames(%q) = %v, want nil", tt.groups, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateGroupNames(%q) = %v, want an error naming %s", tt.groups, err, tt.wantErr)
			}
		})
	}
}

// TestAccessValidatorsRejectNamelessGroups checks that both kinds' access
// validation applies validateGroupNames, not only the helper on its own.
func TestAccessValidatorsRejectNamelessGroups(t *testing.T) {
	tests := []struct {
		name    string
		groups  []string
		wantErr bool
	}{
		{name: "named group is admitted", groups: []string{"llm"}},
		{name: "slash-only group is rejected", groups: []string{"/"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			access := llmv1alpha1.AccessSpec{Groups: tt.groups}
			errs := map[string]error{
				"LLMModel":         validateAccess(&llmv1alpha1.LLMModel{Spec: llmv1alpha1.LLMModelSpec{Access: access}}),
				"PassthroughModel": validatePassthroughAccess(&llmv1alpha1.PassthroughModel{Spec: llmv1alpha1.PassthroughModelSpec{Access: access}}),
			}
			for kind, err := range errs {
				if (err != nil) != tt.wantErr {
					t.Errorf("%s access validation of %q: err = %v, want error %v", kind, tt.groups, err, tt.wantErr)
				}
			}
		})
	}
}
