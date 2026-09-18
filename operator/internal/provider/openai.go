package provider

import (
	"fmt"
	"strings"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func resolveOpenAI(p llmv1alpha1.ProviderSpec) (*Resolved, error) {
	if p.Backend != nil && p.Backend.Bedrock != nil {
		return nil, fmt.Errorf("spec.provider.backend.bedrock is only valid with type=Bedrock")
	}
	if err := requireCredential(p, llmv1alpha1.CredentialAPIKey); err != nil {
		return nil, err
	}
	prefix := p.SchemaVersion
	if prefix == "" {
		prefix = "v1"
	}
	return &Resolved{
		SchemaName: llmv1alpha1.BackendOpenAI,
		// Keep the public legacy setting, but use Envoy's explicit URL prefix.
		SchemaPrefix: "/" + strings.Trim(prefix, "/"),
		SecurityPolicy: map[string]interface{}{
			"type": "APIKey",
			"apiKey": map[string]interface{}{
				"secretRef": map[string]interface{}{"name": p.CredentialSecretName},
			},
		},
	}, nil
}
