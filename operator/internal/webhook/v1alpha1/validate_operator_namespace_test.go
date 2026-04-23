/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"strings"
	"testing"
)

func TestValidateOperatorNamespace(t *testing.T) {
	tests := []struct {
		name              string
		operatorNamespace string
		modelNamespace    string
		wantErr           bool
		wantErrContains   string
	}{
		{
			name:              "empty operator namespace disables check",
			operatorNamespace: "",
			modelNamespace:    "anywhere",
			wantErr:           false,
		},
		{
			name:              "matching namespace is accepted",
			operatorNamespace: "nebari-llm-serving-system",
			modelNamespace:    "nebari-llm-serving-system",
			wantErr:           false,
		},
		{
			name:              "mismatched namespace is rejected",
			operatorNamespace: "nebari-llm-serving-system",
			modelNamespace:    "default",
			wantErr:           true,
			wantErrContains:   "nebari-llm-serving-system",
		},
		{
			name:              "empty model namespace against set operator namespace is rejected",
			operatorNamespace: "nebari-llm-serving-system",
			modelNamespace:    "",
			wantErr:           true,
			wantErrContains:   "nebari-llm-serving-system",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &LLMModelCustomValidator{OperatorNamespace: tt.operatorNamespace}
			err := v.validateOperatorNamespace(tt.modelNamespace)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("expected error to contain %q, got %q", tt.wantErrContains, err.Error())
				}
			} else if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
