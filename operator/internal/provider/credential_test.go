package provider

import (
	"testing"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestUsesCredentialSecretMatchesProviderResolution(t *testing.T) {
	for _, backend := range []string{llmv1alpha1.BackendOpenAI, llmv1alpha1.BackendBedrock} {
		for _, explicit := range []bool{false, true} {
			p := llmv1alpha1.ProviderSpec{
				Backend: &llmv1alpha1.ProviderBackend{Type: backend},
			}
			credentialType := llmv1alpha1.CredentialAPIKey
			if backend == llmv1alpha1.BackendOpenAI {
				p.Hostname = "api.openai.com"
				p.CredentialSecretName = "provider-key"
			} else {
				credentialType = llmv1alpha1.CredentialWorkloadIdentity
				p.Backend.Bedrock = &llmv1alpha1.BedrockBackend{Region: "us-west-2"}
			}
			if explicit {
				p.Credential = &llmv1alpha1.ProviderCredential{Type: credentialType}
			}
			resolved, err := Resolve(p)
			if err != nil {
				t.Fatal(err)
			}
			want := resolved.SecurityPolicy["type"] == "APIKey"
			if got := UsesCredentialSecret(p); got != want {
				t.Errorf("%s, explicit=%t: uses Secret = %t, gateway policy = %v", backend, explicit, got, resolved.SecurityPolicy)
			}
		}
	}
}

func TestUsesCredentialSecretDoesNotRequireValidProvider(t *testing.T) {
	tests := []struct {
		name string
		p    llmv1alpha1.ProviderSpec
		want bool
	}{
		{"legacy missing hostname", llmv1alpha1.ProviderSpec{}, true},
		{"explicit API key", llmv1alpha1.ProviderSpec{Credential: &llmv1alpha1.ProviderCredential{Type: llmv1alpha1.CredentialAPIKey}}, true},
		{"explicit workload identity", llmv1alpha1.ProviderSpec{Credential: &llmv1alpha1.ProviderCredential{Type: llmv1alpha1.CredentialWorkloadIdentity}}, false},
		{"invalid explicit credential", llmv1alpha1.ProviderSpec{Credential: &llmv1alpha1.ProviderCredential{Type: "unsupported"}}, false},
		{"invalid backend", llmv1alpha1.ProviderSpec{Backend: &llmv1alpha1.ProviderBackend{Type: "unsupported"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UsesCredentialSecret(tt.p); got != tt.want {
				t.Fatalf("uses Secret = %t, want %t", got, tt.want)
			}
		})
	}
}
