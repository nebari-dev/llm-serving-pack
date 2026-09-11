package v1alpha1

import (
	"fmt"
	"strings"
)

func (p ProviderSpec) resolveOpenAI() (*ResolvedProvider, error) {
	if p.Backend != nil && p.Backend.Bedrock != nil {
		return nil, fmt.Errorf("spec.provider.backend.bedrock is only valid with type=Bedrock")
	}
	if err := p.requireCredential(CredentialAPIKey); err != nil {
		return nil, err
	}
	prefix := p.SchemaVersion
	if prefix == "" {
		prefix = "v1"
	}
	return &ResolvedProvider{
		SchemaName: BackendOpenAI,
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
