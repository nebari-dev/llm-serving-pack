package reconcilers

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestSlugify(t *testing.T) {
	t.Parallel()

	longInput := strings.Repeat("a", 60)

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, result string)
	}{
		{
			name:  "passthrough already valid",
			input: "devstral-32b",
			check: func(t *testing.T, result string) {
				if result != "devstral-32b" {
					t.Errorf("expected %q, got %q", "devstral-32b", result)
				}
			},
		},
		{
			name:  "lowercase",
			input: "Qwen3-235B",
			check: func(t *testing.T, result string) {
				if result != "qwen3-235b" {
					t.Errorf("expected %q, got %q", "qwen3-235b", result)
				}
			},
		},
		{
			name:  "underscores to dashes",
			input: "my_model_name",
			check: func(t *testing.T, result string) {
				if result != "my-model-name" {
					t.Errorf("expected %q, got %q", "my-model-name", result)
				}
			},
		},
		{
			name:  "dots to dashes",
			input: "model.v1.2",
			check: func(t *testing.T, result string) {
				if result != "model-v1-2" {
					t.Errorf("expected %q, got %q", "model-v1-2", result)
				}
			},
		},
		{
			name:  "truncation with hash suffix",
			input: longInput,
			check: func(t *testing.T, result string) {
				// Should be 48 chars + "-" + 8 char hash = 57 chars
				if len(result) != 57 {
					t.Errorf("expected length 57, got %d (result: %q)", len(result), result)
				}
				if !strings.Contains(result, "-") {
					t.Errorf("expected dash separator in truncated slug, got %q", result)
				}
			},
		},
		{
			name:  "already valid name",
			input: "valid-name",
			check: func(t *testing.T, result string) {
				if result != "valid-name" {
					t.Errorf("expected %q, got %q", "valid-name", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := Slugify(tt.input)
			tt.check(t, result)
		})
	}
}

func TestSlugifyHashCollisionPrevention(t *testing.T) {
	t.Parallel()

	// Two different long inputs should produce different slugs
	input1 := strings.Repeat("a", 60)
	input2 := strings.Repeat("b", 60)

	slug1 := Slugify(input1)
	slug2 := Slugify(input2)

	if slug1 == slug2 {
		t.Errorf("expected different slugs for different inputs, both got %q", slug1)
	}
}

func TestSlugifyDeterminism(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("a", 60)

	result1 := Slugify(input)
	result2 := Slugify(input)
	result3 := Slugify(input)

	if result1 != result2 || result2 != result3 {
		t.Errorf("Slugify is not deterministic: got %q, %q, %q", result1, result2, result3)
	}
}

func TestSlugifyMaxLength(t *testing.T) {
	t.Parallel()

	inputs := []string{
		strings.Repeat("a", 60),
		strings.Repeat("b", 100),
		strings.Repeat("x", 64),
		"short",
		"exactly-forty-eight-characters-long-padded-here",
	}

	for _, input := range inputs {
		result := Slugify(input)
		if len(result) > 63 {
			t.Errorf("slug %q exceeds 63 chars (len=%d) for input %q", result, len(result), input)
		}
	}
}

func TestStandardLabels(t *testing.T) {
	t.Parallel()

	model := &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "devstral-32b",
		},
	}

	labels := StandardLabels(model)

	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{
			name:     "managed-by label",
			key:      "app.kubernetes.io/managed-by",
			expected: "nebari-llm-operator",
		},
		{
			name:     "instance label matches model name",
			key:      "app.kubernetes.io/instance",
			expected: "devstral-32b",
		},
		{
			name:     "name label is llmmodel",
			key:      "app.kubernetes.io/name",
			expected: "llmmodel",
		},
		{
			name:     "llm model label matches model name",
			key:      "llm.nebari.dev/model",
			expected: "devstral-32b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			val, ok := labels[tt.key]
			if !ok {
				t.Errorf("expected label %q to be present", tt.key)
				return
			}
			if val != tt.expected {
				t.Errorf("label %q: expected %q, got %q", tt.key, tt.expected, val)
			}
		})
	}
}

func TestAPIKeySecretName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		modelName string
		expected  string
	}{
		{
			name:      "standard model name",
			modelName: "devstral-32b",
			expected:  "devstral-32b-api-keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := APIKeySecretName(tt.modelName)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestAPIKeyMetadataConfigMapName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		modelName string
		expected  string
	}{
		{
			name:      "standard model name",
			modelName: "devstral-32b",
			expected:  "devstral-32b-api-key-metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := APIKeyMetadataConfigMapName(tt.modelName)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestExternalHostname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		subdomain  string
		baseDomain string
		expected   string
	}{
		{
			name:       "standard subdomain and base domain",
			subdomain:  "devstral",
			baseDomain: "nebari.example.com",
			expected:   "devstral.llm.nebari.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ExternalHostname(tt.subdomain, tt.baseDomain)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
