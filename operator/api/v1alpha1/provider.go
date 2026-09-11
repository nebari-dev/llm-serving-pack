package v1alpha1

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	BackendOpenAI              = "OpenAI"
	BackendBedrock             = "Bedrock"
	CredentialAPIKey           = "APIKey"
	CredentialWorkloadIdentity = "WorkloadIdentity"
)

// ResolvedProvider is the validated input to the shared gateway builders.
// Each backend supplies its address, schema, and upstream authentication;
// routing and access control do not interpret provider-specific fields.
// +kubebuilder:object:generate=false
type ResolvedProvider struct {
	Hostname      string
	Port          int32
	SchemaName    string
	SchemaVersion string
	SchemaPrefix  string
	// SecurityPolicy contains the Envoy AI Gateway upstream auth settings.
	// The shared builder supplies targetRefs, names, labels, and ownership.
	SecurityPolicy map[string]interface{}
}

// EndpointHostname returns the effective address for display clients.
// Provisioning must use Resolve and handle validation errors.
func (p ProviderSpec) EndpointHostname() string {
	resolved, err := p.Resolve()
	if err != nil {
		return p.Hostname
	}
	return resolved.Hostname
}

// Resolve dispatches to a backend variant, then validates the shared address.
// An omitted backend preserves the legacy OpenAI-compatible configuration.
func (p ProviderSpec) Resolve() (*ResolvedProvider, error) {
	backend := BackendOpenAI
	if p.Backend != nil {
		backend = p.Backend.Type
	}
	var resolved *ResolvedProvider
	var err error
	switch backend {
	case BackendOpenAI:
		resolved, err = p.resolveOpenAI()
	case BackendBedrock:
		resolved, err = p.resolveBedrock()
	default:
		return nil, fmt.Errorf("unsupported spec.provider.backend.type %q", backend)
	}
	if err != nil {
		return nil, err
	}
	if p.Hostname != "" {
		resolved.Hostname = p.Hostname
	}
	resolved.Port = p.Port
	if resolved.Port == 0 {
		resolved.Port = 443
	}
	if resolved.Hostname == "" {
		return nil, fmt.Errorf("spec.provider.hostname must not be empty")
	}
	// DNS names are case-insensitive; one terminal dot denotes the DNS root.
	// Normalize the resolved address without rewriting the stored provider spec.
	resolved.Hostname = strings.TrimSuffix(strings.ToLower(resolved.Hostname), ".")
	if problems := validation.IsDNS1123Subdomain(resolved.Hostname); len(problems) > 0 {
		return nil, fmt.Errorf("spec.provider.hostname must be a bare hostname: %s", strings.Join(problems, "; "))
	}
	if resolved.Port < 1 || resolved.Port > 65535 {
		return nil, fmt.Errorf("spec.provider.port must be between 1 and 65535")
	}
	return resolved, nil
}

// requireCredential validates the credential supported by a backend without
// equating workload identity with a particular cloud's gateway policy.
func (p ProviderSpec) requireCredential(credentialType string) error {
	if p.Credential != nil && p.Credential.Type != credentialType {
		return fmt.Errorf("spec.provider.credential.type must be %s for this backend", credentialType)
	}
	if credentialType == CredentialAPIKey && strings.TrimSpace(p.CredentialSecretName) == "" {
		return fmt.Errorf("spec.provider.credentialSecretName must not be empty")
	}
	if credentialType == CredentialWorkloadIdentity && p.CredentialSecretName != "" {
		return fmt.Errorf("spec.provider.credentialSecretName must be omitted for workload identity")
	}
	return nil
}
