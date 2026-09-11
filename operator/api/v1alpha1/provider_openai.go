package v1alpha1

import "fmt"

func (p ProviderSpec) resolveOpenAI() (*ResolvedProvider, error) {
	if p.Backend != nil && p.Backend.Bedrock != nil {
		return nil, fmt.Errorf("spec.provider.backend.bedrock is only valid with type=Bedrock")
	}
	if err := p.requireCredential(CredentialAPIKey); err != nil {
		return nil, err
	}
	version := p.SchemaVersion
	if version == "" {
		version = "v1"
	}
	return &ResolvedProvider{
		SchemaName:    BackendOpenAI,
		SchemaVersion: version,
		SecurityPolicy: map[string]interface{}{
			"type": "APIKey",
			"apiKey": map[string]interface{}{
				"secretRef": map[string]interface{}{"name": p.CredentialSecretName},
			},
		},
	}, nil
}
